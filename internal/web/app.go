package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/directory"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/slurmconfig"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "openhpc_session"
	csrfCookie    = "openhpc_csrf"
)

//go:embed templates/*.html static/*
var assets embed.FS

type DashboardMetrics = cluster.Metrics

type Config struct {
	AdminUsername       string
	AdminPassword       string
	DatabasePath        string
	SecureCookies       bool
	TrustedProxyCIDRs   []string
	Metrics             DashboardMetrics
	MetricsAvailable    bool
	MetricsProvider     cluster.Provider
	NodeProvider        cluster.NodeProvider
	PartitionProvider   cluster.PartitionProvider
	JobProvider         cluster.JobProvider
	JobResourceProvider cluster.JobResourceProvider
	JobCanceler         cluster.JobCanceler
	AccountingProvider  cluster.AccountingProvider
	AssociationProvider cluster.AssociationProvider
	CoreHourProvider    cluster.CoreHourProvider
	DirectoryProvider   directory.Provider
	SettingsStore       *platform.SettingsStore
	PartitionStore      *platform.PartitionStore
	PartitionAdmin      cluster.PartitionAdmin
	NodeAdmin           cluster.NodeAdmin
	SettingsDefaults    map[string]string
	SlurmConfigProvider slurmconfig.Provider
	PlatformUsers       *platform.UserStore
}

type application struct {
	metrics             DashboardMetrics
	metricsAvailable    bool
	metricsProvider     cluster.Provider
	nodeProvider        cluster.NodeProvider
	partitionProvider   cluster.PartitionProvider
	jobProvider         cluster.JobProvider
	jobResourceProvider cluster.JobResourceProvider
	jobCanceler         cluster.JobCanceler
	jobResourceSlots    chan struct{}
	jobOutputSlots      chan struct{}
	accountingProvider  cluster.AccountingProvider
	associationProvider cluster.AssociationProvider
	coreHourProvider    cluster.CoreHourProvider
	directoryProvider   directory.Provider
	slurmConfigProvider slurmconfig.Provider
	directorySlots      chan struct{}
	settingsStore       *platform.SettingsStore
	partitionStore      *platform.PartitionStore
	partitionAdmin      cluster.PartitionAdmin
	nodeAdmin           cluster.NodeAdmin
	settingsDefaults    map[string]string
	platformUsers       *platform.UserStore
	templates           *template.Template
	audit               *platform.AuditStore
	sessions            *sessionStore
	loginAttempts       *loginAttemptStore
	secureCookies       bool
}

type Handler struct {
	handler        http.Handler
	audit          *platform.AuditStore
	settingsStore  *platform.SettingsStore
	partitionStore *platform.PartitionStore
	configCloser   io.Closer
	userStore      *platform.UserStore
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	h.handler.ServeHTTP(response, request)
}

func (h *Handler) Close() error {
	var settingsErr error
	if h.settingsStore != nil {
		settingsErr = h.settingsStore.Close()
	}
	var partitionErr error
	if h.partitionStore != nil {
		partitionErr = h.partitionStore.Close()
	}
	return errors.Join(h.audit.Close(), settingsErr, partitionErr, closeConfigured(h.configCloser), closeConfigured(h.userStore))
}

func closeConfigured(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

type sessionStore struct {
	mu     sync.RWMutex
	tokens map[string]sessionData
}

type sessionData struct {
	ExpiresAt time.Time
	CSRFToken string
	Username  string
	Role      platform.UserRole
}

type principal struct {
	Username string
	Role     platform.UserRole
}

type ldapAuthenticator interface {
	Authenticate(context.Context, string, string) (bool, error)
}

const principalContextKey = "openhpc-principal"

type loginAttemptStore struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

type loginAttempt struct {
	Count       int
	WindowStart time.Time
}

func New(config Config) (http.Handler, error) {
	normalizedUsername := strings.TrimSpace(config.AdminUsername)
	if normalizedUsername == "" || len(normalizedUsername) > 64 || len(config.AdminPassword) < 12 {
		return nil, errors.New("admin username and password are required")
	}
	if config.Metrics.CPUUsage < 0 || config.Metrics.CPUUsage > 100 {
		return nil, errors.New("CPU usage must be between 0 and 100")
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(config.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash admin password: %w", err)
	}
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	databasePath := config.DatabasePath
	if databasePath == "" {
		databasePath = ":memory:"
	}
	audit, err := platform.OpenAuditStore(databasePath)
	if err != nil {
		return nil, err
	}
	userStore := config.PlatformUsers
	if userStore == nil {
		userStore, err = platform.OpenUserStore(":memory:")
		if err != nil {
			_ = audit.Close()
			return nil, err
		}
	}
	if err := userStore.Upsert(context.Background(), platform.PlatformUser{Username: normalizedUsername, PasswordHash: string(passwordHash), Role: platform.RoleAdmin, Enabled: true}); err != nil {
		_ = audit.Close()
		if config.PlatformUsers == nil {
			_ = userStore.Close()
		}
		return nil, err
	}
	partitionStore := config.PartitionStore
	if partitionStore == nil {
		partitionStore, err = platform.OpenPartitionStore(databasePath)
		if err != nil {
			_ = audit.Close()
			if config.PlatformUsers == nil {
				_ = userStore.Close()
			}
			return nil, err
		}
	}
	var configCloser io.Closer
	if closer, ok := config.SlurmConfigProvider.(io.Closer); ok {
		configCloser = closer
	}
	app := &application{
		metrics:             config.Metrics,
		metricsAvailable:    config.MetricsAvailable,
		metricsProvider:     config.MetricsProvider,
		nodeProvider:        config.NodeProvider,
		partitionProvider:   config.PartitionProvider,
		jobProvider:         config.JobProvider,
		jobResourceProvider: config.JobResourceProvider,
		jobCanceler:         config.JobCanceler,
		jobResourceSlots:    make(chan struct{}, maxConcurrentJobResourceReads),
		jobOutputSlots:      make(chan struct{}, maxConcurrentJobOutputReads),
		accountingProvider:  config.AccountingProvider,
		associationProvider: config.AssociationProvider,
		coreHourProvider:    config.CoreHourProvider,
		directoryProvider:   config.DirectoryProvider,
		slurmConfigProvider: config.SlurmConfigProvider,
		directorySlots:      make(chan struct{}, maxConcurrentDirectoryReads),
		settingsStore:       config.SettingsStore,
		partitionStore:      partitionStore,
		partitionAdmin:      config.PartitionAdmin,
		nodeAdmin:           config.NodeAdmin,
		settingsDefaults:    cloneSettings(config.SettingsDefaults),
		platformUsers:       userStore,
		templates:           templates,
		audit:               audit,
		sessions:            &sessionStore{tokens: map[string]sessionData{}},
		loginAttempts:       &loginAttemptStore{attempts: map[string]loginAttempt{}},
		secureCookies:       config.SecureCookies,
	}

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = app.errorHandler
	if err := configureIPExtractor(e, config.TrustedProxyCIDRs); err != nil {
		_ = audit.Close()
		if partitionStore != nil {
			_ = partitionStore.Close()
		}
		_ = closeConfigured(configCloser)
		if config.PlatformUsers == nil {
			_ = userStore.Close()
		}
		return nil, err
	}
	if err := app.syncSystemPartitions(context.Background()); err != nil {
		log.Printf("partition system import failed: %v", err)
	}
	if err := app.syncStoredPartitions(context.Background()); err != nil {
		log.Printf("partition restore sync failed: %v", err)
	}
	e.Use(requestBodyLimit(16 << 10))
	e.Use(securityHeaders)
	e.GET("/login", app.loginPage)
	e.POST("/login", app.login)
	staticFiles, err := fs.Sub(assets, "static")
	if err != nil {
		_ = audit.Close()
		if partitionStore != nil {
			_ = partitionStore.Close()
		}
		_ = closeConfigured(configCloser)
		if config.PlatformUsers == nil {
			_ = userStore.Close()
		}
		return nil, fmt.Errorf("load static files: %w", err)
	}
	e.GET("/static/*", echo.WrapHandler(http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles)))))

	protected := e.Group("")
	protected.Use(app.requireAuthentication)
	protected.Use(app.requireCSRF)
	protected.GET("/", func(c echo.Context) error { return c.Redirect(http.StatusFound, "/dashboard") })
	protected.GET("/dashboard", app.dashboard)
	protected.GET("/slurm/nodes", app.slurmNodes, app.requireAdmin)
	protected.POST("/slurm/nodes/state", app.setNodeAvailability, app.requireAdmin)
	protected.GET("/slurm/partitions", app.slurmPartitions, app.requireAdmin)
	protected.POST("/slurm/partitions", app.savePartition, app.requireAdmin)
	protected.POST("/slurm/partitions/delete", app.deletePartition, app.requireAdmin)
	protected.GET("/slurm/jobs", app.slurmJobs)
	protected.GET("/slurm/jobs/:id/resources", app.slurmJobResources)
	protected.GET("/slurm/jobs/:id/output/:stream", app.slurmJobOutput)
	protected.POST("/slurm/jobs/:id/cancel", app.slurmJobCancel)
	protected.GET("/slurm/accounts", app.slurmAccounts, app.requireAdmin)
	protected.GET("/slurm/associations", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/slurm/accounts#associations")
	}, app.requireAdmin)
	protected.GET("/slurm/qos", app.slurmQoS, app.requireAdmin)
	protected.GET("/slurm/core-hours", func(c echo.Context) error {
		period := c.QueryParam("period")
		target := "/slurm/qos?view=core-hours"
		if period != "" {
			target += "&period=" + url.QueryEscape(period)
		}
		return c.Redirect(http.StatusFound, target)
	}, app.requireAdmin)
	protected.GET("/slurm/config", app.slurmConfig, app.requireAdmin)
	protected.GET("/ldap", app.ldapDirectory, app.requireAdmin)
	protected.POST("/ldap/search", app.ldapDirectorySearch, app.requireAdmin)
	protected.GET("/ldap/users/:uid", app.ldapUser, app.requireAdmin)
	protected.GET("/ldap/groups/:name", app.ldapGroup, app.requireAdmin)
	protected.GET("/settings", app.settingsPage, app.requireAdmin)
	protected.POST("/settings", app.saveSettings, app.requireAdmin)
	protected.POST("/settings/ldap-test", app.testLDAPConnectivity, app.requireAdmin)
	protected.GET("/audit", app.auditLog, app.requireAdmin)
	protected.GET("/platform/users", app.platformUsersPage, app.requireAdmin)
	protected.POST("/platform/users", app.createPlatformUser, app.requireAdmin)
	protected.POST("/platform/users/status", app.setPlatformUserStatus, app.requireAdmin)
	protected.POST("/preferences/language", app.setLanguage)
	protected.POST("/preferences/theme", app.setTheme)
	protected.POST("/logout", app.logout)
	for _, path := range []string{
		"/slurm/users",
		"/system/files", "/terminal",
	} {
		protected.GET(path, app.authorizedPlaceholder)
	}

	return &Handler{handler: e, audit: audit, settingsStore: config.SettingsStore, partitionStore: partitionStore, configCloser: configCloser, userStore: userStore}, nil
}

const auditPageSize = 50

func (a *application) auditLog(c echo.Context) error {
	beforeID, err := parseAuditCursor(c.QueryParam("before_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	lang := language(c)
	labels := auditCopyFor(lang)
	currentModule := moduleByPath("/audit", lang)
	page, queryErr := a.audit.List(c.Request().Context(), beforeID, auditPageSize)
	available := queryErr == nil
	if queryErr != nil {
		log.Printf("audit log query failed")
		page = platform.AuditPage{}
	}
	events := make([]auditEventView, len(page.Events))
	for index, event := range page.Events {
		events[index] = newAuditEventView(event)
	}
	view := auditView{
		appChrome: a.newAppChrome(c, currentModule.Path, a.metricsAvailable, pageHeading{
			Eyebrow: "OPENHPC / " + currentModule.Group, Title: currentModule.Label,
			Description: labels.Description, RefreshPath: "/audit", RefreshLabel: labels.Refresh,
		}),
		Labels: labels, Events: events, AuditAvailable: available,
		HasMore: page.HasMore, NextBeforeID: page.NextBeforeID,
	}
	return a.render(c, http.StatusOK, "audit.html", view)
}

func parseAuditCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) > 19 || value[0] == '0' {
		return 0, errors.New("invalid audit cursor")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("invalid audit cursor")
		}
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor <= 0 {
		return 0, errors.New("invalid audit cursor")
	}
	return cursor, nil
}

func (a *application) slurmAccounts(c echo.Context) error {
	associationPage, err := parseAssociationPage(c.QueryParam("association_page"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	lang := language(c)
	labels := detailCopyFor(lang)
	currentModule := moduleByPath("/slurm/accounts", lang)
	var directory cluster.AccountDirectory
	available := false
	if a.accountingProvider != nil {
		liveDirectory, err := a.accountingProvider.AccountDirectory(c.Request().Context())
		if err != nil {
			log.Printf("Slurm account directory failed")
		} else {
			directory, available = liveDirectory, true
		}
	}
	var associations []cluster.Association
	associationPreviousPage, associationNextPage := 0, 0
	associationsAvailable := false
	if a.associationProvider != nil {
		liveAssociations, err := a.associationProvider.Associations(c.Request().Context())
		if err != nil {
			log.Printf("Slurm associations snapshot failed")
		} else {
			associations, associationPreviousPage, associationNextPage = paginateAssociations(liveAssociations, associationPage)
			associationsAvailable = true
		}
	}
	view := accountsView{
		appChrome: a.newAppChrome(c, currentModule.Path, available, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: currentModule.Label, Description: labels.LiveData,
			RefreshPath: currentModule.Path, RefreshLabel: labels.Refresh,
		}),
		Module: currentModule, Labels: labels, Directory: directory,
		Associations: associations, AssociationsAvailable: associationsAvailable,
		AssociationPreviousPage: associationPreviousPage, AssociationNextPage: associationNextPage,
	}
	return a.render(c, http.StatusOK, "accounts.html", view)
}

const (
	associationPageSize = 100
	maxAssociationPages = 100
)

func parseAssociationPage(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	if len(value) > 3 || value[0] == '0' {
		return 0, errors.New("invalid association page")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("invalid association page")
		}
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 || page > maxAssociationPages {
		return 0, errors.New("invalid association page")
	}
	return page, nil
}

func paginateAssociations(values []cluster.Association, page int) ([]cluster.Association, int, int) {
	start := (page - 1) * associationPageSize
	if start >= len(values) {
		if page > 1 {
			return nil, page - 1, 0
		}
		return nil, 0, 0
	}
	end := start + associationPageSize
	if end > len(values) {
		end = len(values)
	}
	previousPage, nextPage := 0, 0
	if page > 1 {
		previousPage = page - 1
	}
	if end < len(values) {
		nextPage = page + 1
	}
	return append([]cluster.Association(nil), values[start:end]...), previousPage, nextPage
}

func (a *application) slurmQoS(c echo.Context) error {
	lang := language(c)
	labels := detailCopyFor(lang)
	coreLabels := coreHourCopyFor(lang)
	currentModule := moduleByPath("/slurm/qos", lang)
	selectedView := c.QueryParam("view")
	if selectedView == "" {
		selectedView = "qos"
	}
	if selectedView != "qos" && selectedView != "core-hours" {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	period, err := parseCoreHourPeriod(c.QueryParam("period"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	var qos []cluster.QoS
	var coreHours cluster.CoreHourSummary
	available := false
	if selectedView == "qos" && a.accountingProvider != nil {
		liveQoS, err := a.accountingProvider.QoS(c.Request().Context())
		if err != nil {
			log.Printf("Slurm QoS snapshot failed")
		} else {
			qos, available = liveQoS, true
		}
	} else if selectedView == "core-hours" && a.coreHourProvider != nil {
		liveCoreHours, err := a.coreHourProvider.CoreHours(c.Request().Context(), period)
		if err != nil {
			log.Printf("Slurm core-hour snapshot failed")
		} else {
			coreHours, available = liveCoreHours, true
		}
	}
	refreshPath := currentModule.Path
	if selectedView == "core-hours" {
		refreshPath += "?view=core-hours&period=" + string(period)
	}
	view := qosView{
		appChrome: a.newAppChrome(c, currentModule.Path, available, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: currentModule.Label, Description: labels.LiveData,
			RefreshPath: refreshPath, RefreshLabel: labels.Refresh,
		}),
		Module: currentModule, Labels: labels, QoS: qos, ShowCoreHours: selectedView == "core-hours",
		CoreLabels: coreLabels, CoreHours: newCoreHourSummaryView(coreHours), SelectedPeriod: period,
	}
	return a.render(c, http.StatusOK, "qos.html", view)
}

func parseCoreHourPeriod(value string) (cluster.CoreHourPeriod, error) {
	if value == "" || value == string(cluster.CoreHourPeriod24Hours) {
		return cluster.CoreHourPeriod24Hours, nil
	}
	period := cluster.CoreHourPeriod(value)
	if period != cluster.CoreHourPeriod7Days && period != cluster.CoreHourPeriod30Days {
		return "", errors.New("invalid core-hour period")
	}
	return period, nil
}

func (a *application) slurmNodes(c echo.Context) error {
	lang := language(c)
	labels := detailCopyFor(lang)
	var nodes []cluster.Node
	nodesAvailable := false
	if a.nodeProvider != nil {
		liveNodes, err := a.nodeProvider.Nodes(c.Request().Context())
		if err != nil {
			log.Printf("Slurm nodes snapshot failed: %v", err)
		} else {
			nodes, nodesAvailable = liveNodes, true
		}
	}
	nodeSummary := newNodeSummary(nodes)
	currentModule := moduleByPath("/slurm/nodes", lang)
	view := nodesView{
		appChrome: a.newAppChrome(c, currentModule.Path, nodesAvailable, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: currentModule.Label, Description: labels.LiveData,
			RefreshPath: currentModule.Path, RefreshLabel: labels.Refresh,
		}),
		Module: currentModule, Labels: labels, Nodes: nodes,
		NodeSummary: nodeSummary, NodesAvailable: nodesAvailable,
		Success: nodeAvailabilitySuccessFor(lang, c.QueryParam("saved")),
		Error:   nodeAvailabilityErrorFor(lang, c.QueryParam("error")),
	}
	return a.render(c, http.StatusOK, "nodes.html", view)
}

func (a *application) setNodeAvailability(c echo.Context) error {
	if a.nodeAdmin == nil || a.nodeProvider == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	name := strings.TrimSpace(c.FormValue("name"))
	state := cluster.NodeState(strings.TrimSpace(c.FormValue("state")))
	reason := strings.TrimSpace(c.FormValue("reason"))
	if name == "" || !validNodeState(state) {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	if requiresNodeReason(state) && reason == "" {
		return c.Redirect(http.StatusSeeOther, "/slurm/nodes?error=reason_required#nodes")
	}
	if state == cluster.NodeStateResume {
		reason = ""
	}
	nodes, err := a.nodeProvider.Nodes(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	found := false
	for _, node := range nodes {
		if node.Name == name {
			found = true
			break
		}
	}
	if !found {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	auditAction := nodeStateAuditAction(state)
	if err := a.nodeAdmin.SetNodeState(c.Request().Context(), name, state, reason); err != nil {
		log.Printf("Slurm node %s state %s update failed: %v", name, state, err)
		_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: auditAction, Outcome: "failed", CreatedAt: time.Now()})
		return c.Redirect(http.StatusSeeOther, "/slurm/nodes?error=update_failed#nodes")
	}
	if err := a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: auditAction, Outcome: "success", CreatedAt: time.Now()}); err != nil {
		log.Printf("node availability audit failed")
	}
	return c.Redirect(http.StatusSeeOther, "/slurm/nodes?saved="+string(state)+"#nodes")
}

func validNodeState(state cluster.NodeState) bool {
	return state == cluster.NodeStateDown || state == cluster.NodeStateDrain || state == cluster.NodeStateResume
}

func requiresNodeReason(state cluster.NodeState) bool {
	return state == cluster.NodeStateDown || state == cluster.NodeStateDrain
}

func nodeStateAuditAction(state cluster.NodeState) string {
	return "slurm.node.state." + string(state)
}

func nodeAvailabilitySuccessFor(language, saved string) string {
	if saved == string(cluster.NodeStateResume) {
		if language == "en" {
			return "Node brought online."
		}
		return "节点已上线。"
	}
	if saved == string(cluster.NodeStateDown) {
		if language == "en" {
			return "Node taken offline."
		}
		return "节点已下线。"
	}
	if saved == string(cluster.NodeStateDrain) {
		if language == "en" {
			return "Node is draining."
		}
		return "节点已进入 Drain 状态。"
	}
	return ""
}

func nodeAvailabilityErrorFor(language, code string) string {
	if code == "reason_required" {
		if language == "en" {
			return "A reason is required when taking a node offline or draining it."
		}
		return "下线或 Drain 节点时必须填写原因。"
	}
	if code != "update_failed" {
		return ""
	}
	if language == "en" {
		return "Node availability could not be updated."
	}
	return "节点可用状态更新失败。"
}

func (a *application) slurmJobs(c echo.Context) error {
	lang := language(c)
	labels := detailCopyFor(lang)
	var jobs []cluster.Job
	available := false
	if a.jobProvider != nil {
		liveJobs, err := a.jobProvider.Jobs(c.Request().Context())
		if err != nil {
			log.Printf("Slurm jobs snapshot failed")
		} else {
			jobs, available = filterJobsForPrincipal(liveJobs, currentPrincipal(c)), true
		}
	}
	currentModule := moduleByPath("/slurm/jobs", lang)
	jobDetails := make([]jobModalView, len(jobs))
	for index, job := range jobs {
		endTime := job.EndTime
		if lang != "en" && strings.EqualFold(endTime, "Unknown") {
			endTime = labels.Unknown
		}
		jobDetails[index] = jobModalView{
			Labels: labels, Job: job, EndTime: endTime,
		}
	}
	jobRows := make([]jobView, len(jobs))
	for index, job := range jobs {
		jobRows[index] = jobView{Job: job, CanCancel: a.jobCanceler != nil && canCancelJob(currentPrincipal(c), job)}
	}
	view := jobsView{
		appChrome: a.newAppChrome(c, currentModule.Path, available, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: currentModule.Label, Description: labels.LiveData,
			RefreshPath: currentModule.Path, RefreshLabel: labels.Refresh,
		}),
		Module: currentModule, Labels: labels, Jobs: jobRows, JobDetails: jobDetails,
	}
	return a.render(c, http.StatusOK, "jobs.html", view)
}

func (a *application) loginPage(c echo.Context) error {
	lang := language(c)
	return a.render(c, http.StatusOK, "login.html", loginView{Language: lang, Theme: "research-red", PageTitle: signInTitle(lang), Next: safeNext(c.QueryParam("next"))})
}

func (a *application) login(c echo.Context) error {
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	next := safeNext(c.FormValue("next"))
	if username == "" || len(username) > 64 || password == "" || len(password) > 256 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid credentials format")
	}
	clientKey := c.RealIP()
	if !a.loginAttempts.reserve(clientKey, time.Now()) {
		return echo.NewHTTPError(http.StatusTooManyRequests, "too many login attempts")
	}
	var user platform.PlatformUser
	found := false
	var lookupErr error
	if platform.ValidateUsername(username) == nil {
		user, found, lookupErr = a.platformUsers.Get(c.Request().Context(), username)
	}
	if lookupErr != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	passwordMatches := found && user.Enabled && bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
	ldapMatches := false
	if !passwordMatches && !found {
		ldapMatches, lookupErr = a.authenticateLDAPUser(c.Request().Context(), username, password)
		if lookupErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
	}
	if !passwordMatches && !ldapMatches {
		if err := a.recordAudit(c, platform.AuditEvent{Actor: username, Action: "auth.login", Outcome: "denied", CreatedAt: time.Now()}); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		errorMessage := "用户名或密码错误"
		if language(c) == "en" {
			errorMessage = "Invalid username or password"
		}
		return a.render(c, http.StatusUnauthorized, "login.html", loginView{Language: language(c), Theme: "research-red", PageTitle: signInTitle(language(c)), Next: next, Error: errorMessage})
	}

	token, err := randomToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	csrfToken, err := randomToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	if err := a.recordAudit(c, platform.AuditEvent{Actor: username, Action: "auth.login", Outcome: "success", CreatedAt: time.Now()}); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	a.loginAttempts.reset(clientKey)
	role := user.Role
	if ldapMatches {
		role = platform.RoleUser
	}
	a.sessions.add(token, sessionData{ExpiresAt: time.Now().Add(12 * time.Hour), CSRFToken: csrfToken, Username: username, Role: role})
	http.SetCookie(c.Response(), &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: 12 * 60 * 60,
		HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Response(), &http.Cookie{
		Name: csrfCookie, Value: csrfToken, Path: "/", MaxAge: 12 * 60 * 60,
		HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode,
	})
	return c.Redirect(http.StatusSeeOther, next)
}

func (a *application) authenticateLDAPUser(ctx context.Context, username, password string) (bool, error) {
	if a.directoryProvider == nil {
		return false, nil
	}
	authenticator, ok := a.directoryProvider.(ldapAuthenticator)
	if !ok {
		return false, nil
	}
	return authenticator.Authenticate(ctx, username, password)
}

func (a *application) dashboard(c echo.Context) error {
	lang := language(c)
	localizedCopy := copyFor(lang)
	metrics := a.metrics
	metricsAvailable := a.metricsAvailable
	if a.metricsProvider != nil {
		liveMetrics, err := a.metricsProvider.Snapshot(c.Request().Context())
		if err != nil {
			log.Printf("Slurm metrics snapshot failed: %v", err)
			metrics = DashboardMetrics{}
			metricsAvailable = false
		} else {
			metrics = liveMetrics
			metricsAvailable = true
		}
	}
	view := dashboardView{
		appChrome: a.newAppChrome(c, "/dashboard", metricsAvailable, pageHeading{
			Eyebrow: "OPENHPC / " + localizedCopy.DashboardLabel,
			Title:   localizedCopy.Overview, Description: localizedCopy.UpdatedNow,
			Status: func() string {
				if metricsAvailable {
					return localizedCopy.SystemHealthy
				}
				return localizedCopy.SlurmUnavailable
			}(),
			StatusAvailable: metricsAvailable,
		}),
		Metrics: metrics, MetricsAvailable: metricsAvailable,
	}
	return a.render(c, http.StatusOK, "dashboard.html", view)
}

func (a *application) modulePlaceholder(c echo.Context) error {
	lang := language(c)
	localizedCopy := copyFor(lang)
	currentModule := moduleByPath(c.Path(), lang)
	available := a.slurmHealth(c.Request().Context())
	view := moduleView{
		appChrome: a.newAppChrome(c, currentModule.Path, available, pageHeading{
			Eyebrow: "OPENHPC / " + currentModule.Group, Title: currentModule.Label,
			Description: localizedCopy.ComingSoonDetail,
		}),
		Module: currentModule,
	}
	return a.render(c, http.StatusOK, "module.html", view)
}

func (a *application) authorizedPlaceholder(c echo.Context) error {
	if currentPrincipal(c).Role != platform.RoleAdmin && c.Path() != "/system/files" && c.Path() != "/terminal" {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	return a.modulePlaceholder(c)
}

func filterJobsForPrincipal(jobs []cluster.Job, identity principal) []cluster.Job {
	if identity.Role == platform.RoleAdmin {
		return jobs
	}
	filtered := make([]cluster.Job, 0, len(jobs))
	for _, job := range jobs {
		if job.User == identity.Username {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func canAccessJob(identity principal, job cluster.Job) bool {
	return identity.Role == platform.RoleAdmin || job.User == identity.Username
}

func canCancelJob(identity principal, job cluster.Job) bool {
	return identity.Role == platform.RoleAdmin || job.User == identity.Username
}

func (a *application) slurmHealth(ctx context.Context) bool {
	available := a.metricsAvailable
	if a.metricsProvider == nil {
		return available
	}
	if _, err := a.metricsProvider.Snapshot(ctx); err != nil {
		log.Printf("Slurm metrics health check failed")
		return false
	}
	return true
}

func (a *application) newAppChrome(c echo.Context, activePath string, available bool, heading pageHeading) appChrome {
	lang := language(c)
	identity := currentPrincipal(c)
	roleLabel := copyFor(lang).PlatformAdmin
	if identity.Role != platform.RoleAdmin {
		if lang == "en" {
			roleLabel = "Platform user"
		} else {
			roleLabel = "平台用户"
		}
	}
	return appChrome{
		Language: lang, Theme: theme(c), Username: identity.Username, RoleLabel: roleLabel, CSRFToken: a.csrfToken(c),
		PageTitle: heading.Title, ActivePath: activePath, Available: available,
		Copy: copyFor(lang), Modules: modulesForRole(lang, identity.Role), Heading: heading,
	}
}

func signInTitle(language string) string {
	if language == "en" {
		return "Sign in"
	}
	return "登录"
}

func (a *application) setLanguage(c echo.Context) error {
	value := c.FormValue("language")
	if value != "zh" && value != "en" {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported language")
	}
	a.setPreferenceCookie(c, "openhpc_language", value)
	return c.Redirect(http.StatusSeeOther, "/dashboard")
}

func (a *application) setTheme(c echo.Context) error {
	value := c.FormValue("theme")
	if value != "research-red" && value != "slurm-blue" {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported theme")
	}
	a.setPreferenceCookie(c, "openhpc_theme", value)
	return c.Redirect(http.StatusSeeOther, "/dashboard")
}

func (a *application) logout(c echo.Context) error {
	if cookie, err := c.Cookie(sessionCookie); err == nil {
		a.sessions.remove(cookie.Value)
	}
	http.SetCookie(c.Response(), &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Response(), &http.Cookie{
		Name: csrfCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode,
	})
	if err := a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "auth.logout", Outcome: "success", CreatedAt: time.Now()}); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return c.Redirect(http.StatusSeeOther, "/login")
}

func (a *application) requireAuthentication(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cookie, err := c.Cookie(sessionCookie)
		if err != nil {
			return c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(c.Request().URL.Path))
		}
		identity, valid := a.sessions.identity(cookie.Value, time.Now())
		if !valid {
			return c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(c.Request().URL.Path))
		}
		localUser, found, err := a.platformUsers.Get(context.WithoutCancel(c.Request().Context()), identity.Username)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		if found && !localUser.Enabled {
			a.sessions.remove(cookie.Value)
			return c.Redirect(http.StatusFound, "/login?next="+url.QueryEscape(c.Request().URL.Path))
		}
		c.Set(principalContextKey, identity)
		return next(c)
	}
}

func currentPrincipal(c echo.Context) principal {
	if value, ok := c.Get(principalContextKey).(principal); ok {
		return value
	}
	return principal{}
}

func (a *application) requireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if currentPrincipal(c).Role != platform.RoleAdmin {
			return echo.NewHTTPError(http.StatusForbidden)
		}
		return next(c)
	}
}

func (a *application) requireCSRF(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		request := c.Request()
		if request.Method == http.MethodGet || request.Method == http.MethodHead || request.Method == http.MethodOptions {
			return next(c)
		}
		session, sessionErr := c.Cookie(sessionCookie)
		csrf, csrfErr := c.Cookie(csrfCookie)
		if sessionErr != nil || csrfErr != nil {
			return echo.NewHTTPError(http.StatusForbidden, "invalid CSRF token")
		}
		storedToken, exists := a.sessions.csrf(session.Value, time.Now())
		formToken := c.FormValue("_csrf")
		if !exists || !constantTimeEqual(storedToken, csrf.Value) || !constantTimeEqual(storedToken, formToken) {
			return echo.NewHTTPError(http.StatusForbidden, "invalid CSRF token")
		}
		return next(c)
	}
}

func (a *application) render(c echo.Context, status int, name string, data any) error {
	var output bytes.Buffer
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		return err
	}
	return c.HTMLBlob(status, output.Bytes())
}

func (a *application) errorHandler(err error, c echo.Context) {
	code := http.StatusInternalServerError
	var httpError *echo.HTTPError
	if errors.As(err, &httpError) {
		code = httpError.Code
	}
	if !c.Response().Committed {
		http.Error(c.Response(), publicErrorMessage(code, language(c)), code)
	}
}

func publicErrorMessage(code int, lang string) string {
	if lang == "en" {
		switch code {
		case http.StatusBadRequest:
			return "Invalid request"
		case http.StatusForbidden:
			return "Request denied"
		case http.StatusRequestEntityTooLarge:
			return "Request is too large"
		case http.StatusTooManyRequests:
			return "Too many requests. Try again later."
		default:
			return "Request could not be completed"
		}
	}
	switch code {
	case http.StatusBadRequest:
		return "请求参数无效"
	case http.StatusForbidden:
		return "请求已被拒绝"
	case http.StatusRequestEntityTooLarge:
		return "请求内容过大"
	case http.StatusTooManyRequests:
		return "请求过于频繁，请稍后重试"
	default:
		return "请求处理失败"
	}
}

func (a *application) recordAudit(c echo.Context, event platform.AuditEvent) error {
	if err := a.audit.Record(c.Request().Context(), event); err != nil {
		log.Printf("audit write failed for action %s: %v", event.Action, err)
		return err
	}
	return nil
}

func securityHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		header := c.Response().Header()
		header.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		if !strings.HasPrefix(c.Request().URL.Path, "/static/") {
			header.Set("Cache-Control", "no-store")
		}
		return next(c)
	}
}

func requestBodyLimit(limit int64) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			if request.ContentLength > limit {
				return echo.NewHTTPError(http.StatusRequestEntityTooLarge)
			}
			request.Body = http.MaxBytesReader(c.Response(), request.Body, limit)
			return next(c)
		}
	}
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (s *sessionStore) add(token string, data sessionData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := make(map[string]sessionData, len(s.tokens)+1)
	for key, value := range s.tokens {
		if time.Now().Before(value.ExpiresAt) {
			updated[key] = value
		}
	}
	updated[token] = data
	s.tokens = updated
}

func (s *sessionStore) remove(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := make(map[string]sessionData, len(s.tokens))
	for key, value := range s.tokens {
		if key != token {
			updated[key] = value
		}
	}
	s.tokens = updated
}

func (s *sessionStore) removeUsername(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := make(map[string]sessionData, len(s.tokens))
	for token, value := range s.tokens {
		if value.Username != username {
			updated[token] = value
		}
	}
	s.tokens = updated
}

func (s *sessionStore) valid(token string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.tokens[token]
	return exists && now.Before(data.ExpiresAt)
}

func (s *sessionStore) identity(token string, now time.Time) (principal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.tokens[token]
	if !exists || !now.Before(data.ExpiresAt) || data.Username == "" {
		return principal{}, false
	}
	return principal{Username: data.Username, Role: data.Role}, true
}

func (s *sessionStore) csrf(token string, now time.Time) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, exists := s.tokens[token]
	return data.CSRFToken, exists && now.Before(data.ExpiresAt)
}

func (a *application) csrfToken(c echo.Context) string {
	session, err := c.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	token, _ := a.sessions.csrf(session.Value, time.Now())
	return token
}

func constantTimeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (s *loginAttemptStore) reserve(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.attempts[key]
	if !exists || now.Sub(current.WindowStart) >= 15*time.Minute {
		current = loginAttempt{WindowStart: now}
	}
	if current.Count >= 5 {
		return false
	}
	updated := make(map[string]loginAttempt, len(s.attempts)+1)
	for attemptKey, attempt := range s.attempts {
		if now.Sub(attempt.WindowStart) < 15*time.Minute {
			updated[attemptKey] = attempt
		}
	}
	updated[key] = loginAttempt{Count: current.Count + 1, WindowStart: current.WindowStart}
	if len(updated) > 4096 {
		oldestKey := key
		oldestTime := now
		for attemptKey, attempt := range updated {
			if attemptKey != key && !attempt.WindowStart.After(oldestTime) {
				oldestKey = attemptKey
				oldestTime = attempt.WindowStart
			}
		}
		delete(updated, oldestKey)
	}
	s.attempts = updated
	return true
}

func (s *loginAttemptStore) reset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := make(map[string]loginAttempt, len(s.attempts))
	for attemptKey, attempt := range s.attempts {
		if attemptKey != key {
			updated[attemptKey] = attempt
		}
	}
	s.attempts = updated
}

func configureIPExtractor(e *echo.Echo, cidrs []string) error {
	if len(cidrs) == 0 {
		e.IPExtractor = echo.ExtractIPDirect()
		return nil
	}
	options := []echo.TrustOption{
		echo.TrustLoopback(false),
		echo.TrustLinkLocal(false),
		echo.TrustPrivateNet(false),
	}
	for _, cidr := range cidrs {
		_, trustedRange, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return fmt.Errorf("parse trusted proxy CIDR %q: %w", cidr, err)
		}
		options = append(options, echo.TrustIPRange(trustedRange))
	}
	e.IPExtractor = echo.ExtractIPFromXFFHeader(options...)
	return nil
}

func language(c echo.Context) string {
	if cookie, err := c.Cookie("openhpc_language"); err == nil && cookie.Value == "en" {
		return "en"
	}
	return "zh"
}

func theme(c echo.Context) string {
	if cookie, err := c.Cookie("openhpc_theme"); err == nil && cookie.Value == "slurm-blue" {
		return "slurm-blue"
	}
	return "research-red"
}

func (a *application) setPreferenceCookie(c echo.Context, name, value string) {
	http.SetCookie(c.Response(), &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: 365 * 24 * 60 * 60, HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteLaxMode})
}

func safeNext(value string) string {
	if value != "" && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, `\`) {
		return value
	}
	return "/dashboard"
}
