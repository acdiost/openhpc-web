package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/directory"
	"github.com/acdiost/openhpc-web/internal/ldapdirectory"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/slurm"
	"github.com/acdiost/openhpc-web/internal/slurmconfig"
	"github.com/acdiost/openhpc-web/internal/terminal"
	"github.com/acdiost/openhpc-web/internal/web"
)

var newWebHandler = web.New

func main() {
	if handled, err := web.HandleJobOutputReaderInvocation(os.Args[1:], os.Stdout); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := requireRoot(os.Geteuid()); err != nil {
		log.Fatal(err)
	}
	username := os.Getenv("OPENHPC_ADMIN_USERNAME")
	password := os.Getenv("OPENHPC_ADMIN_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("OPENHPC_ADMIN_USERNAME and OPENHPC_ADMIN_PASSWORD are required")
	}

	address := envOr("OPENHPC_ADDRESS", "127.0.0.1:8080")
	secureCookies := os.Getenv("OPENHPC_SECURE_COOKIES") == "true"
	trustedProxyCIDRs := splitList(os.Getenv("OPENHPC_TRUSTED_PROXY_CIDRS"))
	if err := validateDeploymentConfig(address, secureCookies, trustedProxyCIDRs); err != nil {
		log.Fatal(err)
	}
	databasePath := envOr("OPENHPC_DATABASE_PATH", filepath.Join("state", "openhpc.db"))
	stateWarnings, err := prepareStateDirectory(databasePath)
	if err != nil {
		log.Fatalf("prepare state directory: %v", err)
	}
	for _, warning := range stateWarnings {
		log.Print(warning)
	}
	settingsKey, err := parseSettingsKey()
	if err != nil {
		log.Fatal(err)
	}
	settingsStore, err := platform.OpenSettingsStore(databasePath, settingsKey)
	if err != nil {
		log.Fatalf("open settings store: %v", err)
	}
	defer func() {
		if settingsStore != nil {
			_ = settingsStore.Close()
		}
	}()
	slurmEnabled, slurmConfig, err := parseSlurmConfigFromStore(settingsStore)
	if err != nil {
		log.Fatal(err)
	}
	slurmConfig.Warning = func(message string) { log.Print(message) }
	ldapEnabled, ldapConfig, err := parseLDAPConfigFromStore(settingsStore)
	if err != nil {
		log.Fatal(err)
	}
	terminalEnabled, terminalConfig, err := parseTerminalConfigFromStore(settingsStore)
	if err != nil {
		log.Fatal(err)
	}
	var metricsProvider cluster.Provider
	var nodeProvider cluster.NodeProvider
	var partitionProvider cluster.PartitionProvider
	var jobProvider cluster.JobProvider
	var jobResourceProvider cluster.JobResourceProvider
	var jobCanceler cluster.JobCanceler
	var accountingProvider cluster.AccountingProvider
	var associationProvider cluster.AssociationProvider
	var coreHourProvider cluster.CoreHourProvider
	var directoryProvider directory.Provider
	var slurmConfigProvider slurmconfig.Provider
	var partitionAdmin cluster.PartitionAdmin
	var nodeAdmin cluster.NodeAdmin
	var terminalClient terminal.Client
	userStore, userStoreErr := platform.OpenUserStore(databasePath)
	if userStoreErr != nil {
		log.Fatalf("initialize platform users: %v", userStoreErr)
	}
	if slurmEnabled {
		client, clientErr := slurm.New(slurmConfig)
		err = clientErr
		if err != nil {
			log.Fatalf("initialize Slurm integration: %v", err)
		}
		partitionAdmin = client
		nodeAdmin = client
		metricsProvider, nodeProvider, partitionProvider, jobProvider, jobResourceProvider, jobCanceler, accountingProvider, associationProvider, coreHourProvider = client, client, client, client, client, client, client, client, client
		configRoot := envOr("OPENHPC_SLURM_CONFIG_ROOT", "/usr/local/etc")
		configProvider, configErr := slurmconfig.New(configRoot, 1<<20)
		if configErr != nil {
			log.Printf("WARNING: initialize Slurm config browser: %v", configErr)
		} else {
			slurmConfigProvider = configProvider
		}
	}
	if ldapEnabled {
		directoryProvider, err = ldapdirectory.New(ldapConfig)
		if err != nil {
			log.Fatalf("initialize LDAP integration: %v", err)
		}
	}
	if terminalEnabled {
		terminalClient, err = terminal.New(terminalConfig)
		if err != nil {
			log.Fatalf("initialize SSH terminal: %v", err)
		}
	}
	handler, err := buildHandler(web.Config{
		AdminUsername:       username,
		AdminPassword:       password,
		DatabasePath:        databasePath,
		SecureCookies:       secureCookies,
		TrustedProxyCIDRs:   trustedProxyCIDRs,
		MetricsProvider:     metricsProvider,
		NodeProvider:        nodeProvider,
		PartitionProvider:   partitionProvider,
		JobProvider:         jobProvider,
		JobResourceProvider: jobResourceProvider,
		JobCanceler:         jobCanceler,
		AccountingProvider:  accountingProvider,
		AssociationProvider: associationProvider,
		CoreHourProvider:    coreHourProvider,
		DirectoryProvider:   directoryProvider,
		SettingsStore:       settingsStore,
		SettingsDefaults:    settingsDefaultsFromEnv(),
		PartitionAdmin:      partitionAdmin,
		NodeAdmin:           nodeAdmin,
		SlurmConfigProvider: slurmConfigProvider,
		TerminalClient:      terminalClient,
		PlatformUsers:       userStore,
	}, slurmConfigProvider)
	if err != nil {
		_ = userStore.Close()
		log.Fatalf("initialize server: %v", err)
	}
	if closer, ok := handler.(interface{ Close() error }); ok {
		settingsStore = nil
		defer func() {
			if err := closer.Close(); err != nil {
				log.Printf("close application: %v", err)
			}
		}()
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("OpenHPC Web listening on http://%s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func buildHandler(config web.Config, slurmConfigProvider slurmconfig.Provider) (http.Handler, error) {
	handler, err := newWebHandler(config)
	if err != nil {
		if closer, ok := slurmConfigProvider.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	return handler, nil
}

func requireRoot(euid int) error {
	if euid == 0 {
		return nil
	}
	return errors.New("OpenHPC Web must run as root")
}

func parseSlurmConfigFromEnv() (bool, slurm.Config, error) {
	return parseSlurmConfigFromStore(nil)
}

func parseSlurmConfigFromStore(store *platform.SettingsStore) (bool, slurm.Config, error) {
	enabled, err := settingValue(store, "OPENHPC_SLURM_ENABLED", os.Getenv("OPENHPC_SLURM_ENABLED"))
	if err != nil {
		return false, slurm.Config{}, err
	}
	if enabled != "true" {
		return false, slurm.Config{}, nil
	}
	binaryDir, err := settingValue(store, "OPENHPC_SLURM_BIN_DIR", envOr("OPENHPC_SLURM_BIN_DIR", "/usr/local/bin"))
	if err != nil {
		return false, slurm.Config{}, err
	}
	timeoutValue, err := settingValue(store, "OPENHPC_SLURM_TIMEOUT", envOr("OPENHPC_SLURM_TIMEOUT", "3s"))
	if err != nil {
		return false, slurm.Config{}, err
	}
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_TIMEOUT: %w", err)
	}
	maxOutputValue, err := settingValue(store, "OPENHPC_SLURM_MAX_OUTPUT", envOr("OPENHPC_SLURM_MAX_OUTPUT", "2097152"))
	if err != nil {
		return false, slurm.Config{}, err
	}
	maxOutput, err := strconv.Atoi(maxOutputValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_MAX_OUTPUT: %w", err)
	}
	cacheTTLValue, err := settingValue(store, "OPENHPC_SLURM_CACHE_TTL", envOr("OPENHPC_SLURM_CACHE_TTL", "10s"))
	if err != nil {
		return false, slurm.Config{}, err
	}
	cacheTTL, err := time.ParseDuration(cacheTTLValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_CACHE_TTL: %w", err)
	}
	return true, slurm.Config{
		BinaryDir:      binaryDir,
		Timeout:        timeout,
		MaxOutputBytes: maxOutput,
		CacheTTL:       cacheTTL,
	}, nil
}

func parseLDAPConfigFromEnv() (bool, ldapdirectory.Config, error) {
	return parseLDAPConfigFromStore(nil)
}

func parseTerminalConfigFromStore(store *platform.SettingsStore) (bool, terminal.Config, error) {
	enabled, err := settingValue(store, "OPENHPC_TERMINAL_ENABLED", os.Getenv("OPENHPC_TERMINAL_ENABLED"))
	if err != nil {
		return false, terminal.Config{}, err
	}
	if enabled != "true" {
		return false, terminal.Config{}, nil
	}
	address, err := settingValue(store, "OPENHPC_TERMINAL_SSH_ADDRESS", os.Getenv("OPENHPC_TERMINAL_SSH_ADDRESS"))
	if err != nil {
		return false, terminal.Config{}, err
	}
	timeoutValue, err := settingValue(store, "OPENHPC_TERMINAL_TIMEOUT", envOr("OPENHPC_TERMINAL_TIMEOUT", "10s"))
	if err != nil {
		return false, terminal.Config{}, err
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(timeoutValue))
	if err != nil {
		return false, terminal.Config{}, fmt.Errorf("parse OPENHPC_TERMINAL_TIMEOUT: %w", err)
	}
	return true, terminal.Config{Address: strings.TrimSpace(address), Timeout: timeout}, nil
}

func parseLDAPConfigFromStore(store *platform.SettingsStore) (bool, ldapdirectory.Config, error) {
	enabled, err := settingValue(store, "OPENHPC_LDAP_ENABLED", os.Getenv("OPENHPC_LDAP_ENABLED"))
	if err != nil {
		return false, ldapdirectory.Config{}, err
	}
	if enabled != "true" {
		return false, ldapdirectory.Config{}, nil
	}
	endpoint, err := settingValue(store, "OPENHPC_LDAP_URL", os.Getenv("OPENHPC_LDAP_URL"))
	if err != nil {
		return false, ldapdirectory.Config{}, err
	}
	endpoint = strings.TrimSpace(endpoint)
	baseDN, err := settingValue(store, "OPENHPC_LDAP_BASE_DN", os.Getenv("OPENHPC_LDAP_BASE_DN"))
	if err != nil {
		return false, ldapdirectory.Config{}, err
	}
	baseDN = strings.TrimSpace(baseDN)
	if endpoint == "" || baseDN == "" {
		return false, ldapdirectory.Config{}, errors.New("OPENHPC_LDAP_URL and OPENHPC_LDAP_BASE_DN are required")
	}
	timeoutValue, err := settingValue(store, "OPENHPC_LDAP_TIMEOUT", envOr("OPENHPC_LDAP_TIMEOUT", "3s"))
	if err != nil {
		return false, ldapdirectory.Config{}, err
	}
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("parse OPENHPC_LDAP_TIMEOUT: %w", err)
	}
	maxResultsValue, err := settingValue(store, "OPENHPC_LDAP_MAX_RESULTS", envOr("OPENHPC_LDAP_MAX_RESULTS", "200"))
	if err != nil {
		return false, ldapdirectory.Config{}, err
	}
	maxResults, err := strconv.Atoi(maxResultsValue)
	if err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("parse OPENHPC_LDAP_MAX_RESULTS: %w", err)
	}
	config := ldapdirectory.Config{
		URL: endpoint, BaseDN: baseDN,
		Timeout: timeout, MaxResults: maxResults,
		AllowInsecure: strings.EqualFold(strings.TrimSpace(os.Getenv("OPENHPC_LDAP_ALLOW_INSECURE")), "true"),
	}
	for key, target := range map[string]*string{"OPENHPC_LDAP_USER_BASE_DN": &config.UserBaseDN, "OPENHPC_LDAP_GROUP_BASE_DN": &config.GroupBaseDN, "OPENHPC_LDAP_BIND_DN": &config.BindDN, "OPENHPC_LDAP_BIND_PASSWORD": &config.BindPassword, "OPENHPC_LDAP_PROVISION_BIND_DN": &config.ProvisionBindDN, "OPENHPC_LDAP_PROVISION_BIND_PASSWORD": &config.ProvisionBindPassword, "OPENHPC_LDAP_CA_FILE": &config.CAFile} {
		value, err := settingValue(store, key, os.Getenv(key))
		if err != nil {
			return false, ldapdirectory.Config{}, err
		}
		if key != "OPENHPC_LDAP_BIND_PASSWORD" && key != "OPENHPC_LDAP_PROVISION_BIND_PASSWORD" {
			value = strings.TrimSpace(value)
		}
		*target = value
	}
	if err := ldapdirectory.ValidateConfig(config); err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("validate LDAP configuration: %w", err)
	}
	if config.AllowInsecure {
		log.Print("WARNING: LDAP insecure mode enabled; read and provisioning Bind credentials will be sent without TLS")
	}
	return true, config, nil
}

func parseSettingsKey() ([]byte, error) {
	value := strings.TrimSpace(os.Getenv("OPENHPC_SETTINGS_KEY"))
	if value == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("OPENHPC_SETTINGS_KEY must be base64-encoded 32 bytes")
	}
	return key, nil
}

func settingValue(store *platform.SettingsStore, key, fallback string) (string, error) {
	if store == nil {
		return fallback, nil
	}
	value, found, err := store.Get(context.Background(), key)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	if found {
		return value, nil
	}
	return fallback, nil
}

func settingsDefaultsFromEnv() map[string]string {
	result := make(map[string]string)
	for _, key := range platform.KnownSettingKeys() {
		result[key] = os.Getenv(key)
	}
	if result["OPENHPC_SLURM_BIN_DIR"] == "" {
		result["OPENHPC_SLURM_BIN_DIR"] = "/usr/local/bin"
	}
	if result["OPENHPC_SLURM_TIMEOUT"] == "" {
		result["OPENHPC_SLURM_TIMEOUT"] = "3s"
	}
	if result["OPENHPC_SLURM_MAX_OUTPUT"] == "" {
		result["OPENHPC_SLURM_MAX_OUTPUT"] = "2097152"
	}
	if result["OPENHPC_SLURM_CACHE_TTL"] == "" {
		result["OPENHPC_SLURM_CACHE_TTL"] = "10s"
	}
	if result["OPENHPC_LDAP_TIMEOUT"] == "" {
		result["OPENHPC_LDAP_TIMEOUT"] = "3s"
	}
	if result["OPENHPC_LDAP_MAX_RESULTS"] == "" {
		result["OPENHPC_LDAP_MAX_RESULTS"] = "200"
	}
	if result["OPENHPC_TERMINAL_TIMEOUT"] == "" {
		result["OPENHPC_TERMINAL_TIMEOUT"] = "10s"
	}
	return result
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.TrimSpace(part); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateDeploymentConfig(address string, secureCookies bool, trustedProxyCIDRs []string) error {
	if !isLoopbackAddress(address) {
		return errors.New("OPENHPC_ADDRESS must be a loopback listener behind a local TLS reverse proxy")
	}
	if secureCookies && len(trustedProxyCIDRs) == 0 {
		return errors.New("OPENHPC_TRUSTED_PROXY_CIDRS is required when secure cookies are enabled")
	}
	return nil
}

func prepareStateDirectory(databasePath string) ([]string, error) {
	if databasePath == ":memory:" {
		return nil, nil
	}
	directory := filepath.Dir(databasePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", directory, err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", directory, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a real directory", directory)
	}
	return stateDirectoryWarnings(info, os.Geteuid()), nil
}

func stateDirectoryWarnings(info os.FileInfo, effectiveUID int) []string {
	warnings := make([]string, 0, 2)
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, "WARNING: state directory permissions are broader than 0700; relying on the running user's operating-system permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != effectiveUID {
		warnings = append(warnings, "WARNING: state directory owner differs from the running user; relying on the running user's operating-system permissions")
	}
	return warnings
}
