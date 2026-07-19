package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func TestAccountsPageShowsAccountsAndUsersInApplicationShell(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{
		Accounts: []cluster.Account{{Name: "jfzx", Description: "<research>", Organization: "lab", AssociationCount: 2}},
		Users:    []cluster.SlurmUser{{Name: "alice", AdministratorLevel: "None", DefaultAccount: "jfzx", AssociationCount: 1}},
	}, associations: []cluster.Association{
		{ID: 41, Cluster: "hpc<script>", Account: "jfzx", User: "alice&", Partition: `GPU"`},
		{ID: 42, Cluster: "hpc", Account: "jfzx"},
	}}
	handler := newAccountingHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/accounts", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		`class="app-shell"`, "账户与用户", "jfzx", "&lt;research&gt;", "alice", "默认账户",
		`id="associations"`, "关联明细", "集群", "hpc&lt;script&gt;", "alice&amp;", "GPU&#34;", "账户级", "全部分区",
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "<research>")
	if provider.associationCalls != 1 {
		t.Errorf("association calls = %d, want 1", provider.associationCalls)
	}
}

func TestAccountsPageShowsIndependentAssociationUnavailableState(t *testing.T) {
	provider := &stubAccountingProvider{
		directory:      cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}},
		associationErr: errors.New("/secret/slurm association credential"),
	}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/accounts", "zh")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "research")
	assertBodyContains(t, response, "关联数据暂不可用")
	assertBodyNotContains(t, response, "/secret/slurm")
}

func TestAccountsPageShowsEmptyAssociationState(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}}}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/accounts", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Association details")
	assertBodyContains(t, response, "No associations reported")
}

func TestAccountsPagePaginatesAssociations(t *testing.T) {
	associations := make([]cluster.Association, 101)
	for index := range associations {
		associations[index] = cluster.Association{
			ID: int64(index + 1), Cluster: "hpc", Account: "research", User: "user-" + strconv.Itoa(index),
		}
	}
	handler := newAccountingHandler(t, &stubAccountingProvider{
		directory: cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}}, associations: associations,
	})
	first := getAuthenticated(t, handler, "/slurm/accounts", "zh")
	assertStatus(t, first, http.StatusOK)
	assertBodyContains(t, first, "user-0")
	assertBodyNotContains(t, first, "user-100")
	assertBodyContains(t, first, `href="/slurm/accounts?association_page=2#associations"`)

	second := getAuthenticated(t, handler, "/slurm/accounts?association_page=2", "zh")
	assertStatus(t, second, http.StatusOK)
	assertBodyContains(t, second, "user-100")
	assertBodyNotContains(t, second, "user-0</td>")
	assertBodyContains(t, second, `href="/slurm/accounts?association_page=1#associations"`)
}

func TestAccountsPageRejectsInvalidAssociationPagesBeforeProviderCalls(t *testing.T) {
	provider := &stubAccountingProvider{}
	handler := newAccountingHandler(t, provider)
	session := login(t, handler)
	for _, page := range []string{"0", "-1", "01", "abc", "101", "9223372036854775808"} {
		request := httptest.NewRequest(http.MethodGet, "/slurm/accounts?association_page="+page, nil)
		request.AddCookie(session)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusBadRequest)
	}
	if provider.associationCalls != 0 {
		t.Fatalf("association calls = %d, want 0", provider.associationCalls)
	}
}

func TestAccountsPageKeepsPreviousLinkOnEmptyLaterPage(t *testing.T) {
	handler := newAccountingHandler(t, &stubAccountingProvider{
		directory:    cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}},
		associations: []cluster.Association{{ID: 1, Cluster: "hpc", Account: "research"}},
	})
	response := getAuthenticated(t, handler, "/slurm/accounts?association_page=2", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "No associations reported")
	assertBodyContains(t, response, `href="/slurm/accounts?association_page=1#associations"`)
}

func TestAssociationsRouteRedirectsIntoAccountsPage(t *testing.T) {
	handler := newAccountingHandler(t, &stubAccountingProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/associations", nil)
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/slurm/accounts#associations")
}

func TestQoSPageShowsLiveQoSInApplicationShell(t *testing.T) {
	provider := &stubAccountingProvider{qos: []cluster.QoS{{Name: "normal", Description: "Default", Priority: 10, UsageFactor: 1.5, MaxJobsUnlimited: true}}}
	handler := newAccountingHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/qos", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{`class="app-shell"`, "QoS", "normal", "Default", "1.5", "Unlimited"} {
		assertBodyContains(t, response, value)
	}
}

func TestQoSPageEmbedsCoreHoursWithFixedPeriodControls(t *testing.T) {
	provider := &stubAccountingProvider{coreHours: cluster.CoreHourSummary{
		CoreSeconds: 21_600, AllocationCount: 3,
		Accounts: []cluster.CoreHourGroup{{Name: "research", CoreSeconds: 18_000, AllocationCount: 2}},
		Users:    []cluster.CoreHourGroup{{Name: "alice<script>", CoreSeconds: 14_400, AllocationCount: 1}},
	}}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/qos?view=core-hours&period=7d", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"QoS", "核时统计", "过去 24 小时", "过去 7 天", "过去 30 天", "6.00", "research", "alice&lt;script&gt;", "分配 CPU 核时"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "alice<script>")
	if provider.coreHourPeriod != cluster.CoreHourPeriod7Days {
		t.Errorf("period = %q, want 7d", provider.coreHourPeriod)
	}
}

func TestQoSPageRejectsInvalidCoreHourPeriodBeforeProviderCall(t *testing.T) {
	provider := &stubAccountingProvider{}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/qos?view=core-hours&period=custom", "en")
	assertStatus(t, response, http.StatusBadRequest)
	if provider.coreHourCalls != 0 {
		t.Fatalf("core-hour calls = %d, want 0", provider.coreHourCalls)
	}
}

func TestCoreHoursRouteRedirectsIntoQoSPage(t *testing.T) {
	handler := newAccountingHandler(t, &stubAccountingProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/core-hours?period=30d", nil)
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/slurm/qos?view=core-hours&period=30d")
}

func TestAccountingPagesShowUnavailableStateWithoutLeakingProviderErrors(t *testing.T) {
	provider := &stubAccountingProvider{err: errors.New("sacctmgr password=secret failed")}
	handler := newAccountingHandler(t, provider)
	for _, path := range []string{"/slurm/accounts", "/slurm/qos"} {
		response := getAuthenticated(t, handler, path, "en")
		assertStatus(t, response, http.StatusOK)
		assertBodyContains(t, response, "Slurm data is temporarily unavailable")
		assertBodyNotContains(t, response, "password=secret")
	}
}

func TestAccountingPagesRequireAuthentication(t *testing.T) {
	handler := newAccountingHandler(t, &stubAccountingProvider{})
	for _, path := range []string{"/slurm/accounts", "/slurm/qos", "/slurm/core-hours", "/slurm/associations"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusFound)
		assertHeader(t, response, "Location", "/login?next="+url.QueryEscape(path))
	}
}

func newAccountingHandler(t *testing.T, provider cluster.AccountingProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword,
		AccountingProvider: provider, AssociationProvider: associationProvider(provider), CoreHourProvider: coreHourProvider(provider),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func coreHourProvider(provider cluster.AccountingProvider) cluster.CoreHourProvider {
	value, _ := provider.(cluster.CoreHourProvider)
	return value
}

type stubAccountingProvider struct {
	directory        cluster.AccountDirectory
	qos              []cluster.QoS
	associations     []cluster.Association
	err              error
	associationErr   error
	associationCalls int
	coreHours        cluster.CoreHourSummary
	coreHourPeriod   cluster.CoreHourPeriod
	coreHourCalls    int
}

func (p *stubAccountingProvider) AccountDirectory(context.Context) (cluster.AccountDirectory, error) {
	return p.directory, p.err
}
func (p *stubAccountingProvider) QoS(context.Context) ([]cluster.QoS, error) {
	return append([]cluster.QoS(nil), p.qos...), p.err
}

func (p *stubAccountingProvider) Associations(context.Context) ([]cluster.Association, error) {
	p.associationCalls++
	return append([]cluster.Association(nil), p.associations...), p.associationErr
}

func (p *stubAccountingProvider) CoreHours(_ context.Context, period cluster.CoreHourPeriod) (cluster.CoreHourSummary, error) {
	p.coreHourCalls++
	p.coreHourPeriod = period
	return p.coreHours, p.err
}

func associationProvider(provider cluster.AccountingProvider) cluster.AssociationProvider {
	value, _ := provider.(cluster.AssociationProvider)
	return value
}
