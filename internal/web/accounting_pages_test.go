package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
	accounts := getAuthenticated(t, handler, "/slurm/accounts", "zh")
	assertStatus(t, accounts, http.StatusOK)
	for _, value := range []string{`class="app-shell"`, "账户与用户", "jfzx", "&lt;research&gt;"} {
		assertBodyContains(t, accounts, value)
	}
	assertBodyNotContains(t, accounts, "<research>")

	users := getAuthenticated(t, handler, "/slurm/accounts?tab=users", "zh")
	assertStatus(t, users, http.StatusOK)
	assertBodyContains(t, users, "alice")
	assertBodyContains(t, users, "默认账户")

	associations := getAuthenticated(t, handler, "/slurm/accounts?tab=associations", "zh")
	assertStatus(t, associations, http.StatusOK)
	for _, value := range []string{`id="associations"`, "关联明细", "集群", "hpc&lt;script&gt;", "alice&amp;", "GPU&#34;", "账户级", "全部分区"} {
		assertBodyContains(t, associations, value)
	}
	if provider.associationCalls != 3 {
		t.Errorf("association calls = %d, want 3", provider.associationCalls)
	}
}

func TestAccountsPageShowsDirectorySummaryAndAccessibleAssociationLinks(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{
		Accounts: []cluster.Account{
			{Name: "research", AssociationCount: 3},
			{Name: "training", AssociationCount: 1},
		},
		Users: []cluster.SlurmUser{
			{Name: "alice", AssociationCount: 2},
			{Name: "bob", AssociationCount: 1},
			{Name: "chen", AssociationCount: 1},
		},
	}, associations: []cluster.Association{
		{ID: 1, Cluster: "hpc", Account: "research", User: "alice"},
		{ID: 2, Cluster: "hpc", Account: "research", User: "bob"},
		{ID: 3, Cluster: "hpc", Account: "research", User: "chen"},
		{ID: 4, Cluster: "hpc", Account: "training"},
	}}

	handler := newAccountingHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/accounts", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		`class="account-summary"`, `class="account-section-count">2</span>`, `aria-label="research Associations: 3"`,
		`href="/slurm/accounts?tab=associations#associations"`,
	} {
		assertBodyContains(t, response, value)
	}
	users := getAuthenticated(t, handler, "/slurm/accounts?tab=users", "en")
	assertBodyContains(t, users, `class="account-section-count">3</span>`)
	assertBodyContains(t, users, `aria-label="alice Associations: 2"`)
}

func TestAccountsPageRendersOnlyTheSelectedTab(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{
		Accounts: []cluster.Account{{Name: "research"}},
		Users:    []cluster.SlurmUser{{Name: "alice"}},
	}, associations: []cluster.Association{{ID: 1, Cluster: "hpc", Account: "research", User: "alice"}}}
	handler := newAccountingHandler(t, provider)

	accounts := getAuthenticated(t, handler, "/slurm/accounts", "en")
	assertStatus(t, accounts, http.StatusOK)
	assertBodyContains(t, accounts, `data-account-tab="accounts" href="/slurm/accounts?tab=accounts" class="active" aria-current="page"`)
	assertBodyContains(t, accounts, `id="accounts-panel"`)
	assertBodyNotContains(t, accounts, `id="users-panel"`)
	assertBodyNotContains(t, accounts, `id="associations-panel"`)
	assertBodyContains(t, accounts, `href="/slurm/accounts?tab=associations&association_account=research#associations"`)

	users := getAuthenticated(t, handler, "/slurm/accounts?tab=users", "en")
	assertStatus(t, users, http.StatusOK)
	assertBodyContains(t, users, `data-account-tab="users" href="/slurm/accounts?tab=users" class="active" aria-current="page"`)
	assertBodyContains(t, users, `id="users-panel"`)
	assertBodyNotContains(t, users, `id="accounts-panel"`)
	assertBodyContains(t, users, `href="/slurm/accounts?tab=associations&association_user=alice#associations"`)

	associations := getAuthenticated(t, handler, "/slurm/accounts?tab=associations", "en")
	assertStatus(t, associations, http.StatusOK)
	assertBodyContains(t, associations, `data-account-tab="associations" href="/slurm/accounts?tab=associations#associations" class="active" aria-current="page"`)
	assertBodyContains(t, associations, `id="associations-panel"`)
	assertBodyNotContains(t, associations, `id="accounts-panel"`)
}

func TestAccountsPageInfersAssociationsTabForLegacyDeepLinks(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{
		Accounts: []cluster.Account{{Name: "research"}},
	}, associations: []cluster.Association{{ID: 1, Cluster: "hpc", Account: "research", User: "alice"}}}

	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/accounts?association_account=research", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, `data-account-tab="associations" href="/slurm/accounts?tab=associations#associations" class="active" aria-current="page"`)
	assertBodyContains(t, response, `id="associations-panel"`)
	assertBodyNotContains(t, response, `id="accounts-panel"`)
}

func TestAccountsPageShowsIndependentAssociationUnavailableState(t *testing.T) {
	provider := &stubAccountingProvider{
		directory:      cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}},
		associationErr: errors.New("/secret/slurm association credential"),
	}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/accounts?tab=associations", "zh")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "research")
	assertBodyContains(t, response, "关联数据暂不可用")
	assertBodyNotContains(t, response, "/secret/slurm")
}

func TestAccountsPageShowsEmptyAssociationState(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{Accounts: []cluster.Account{{Name: "research"}}}}
	response := getAuthenticated(t, newAccountingHandler(t, provider), "/slurm/accounts?tab=associations", "en")
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
	first := getAuthenticated(t, handler, "/slurm/accounts?tab=associations", "zh")
	assertStatus(t, first, http.StatusOK)
	assertBodyContains(t, first, "user-0")
	assertBodyNotContains(t, first, "user-100")
	assertBodyContains(t, first, `href="/slurm/accounts?association_page=2&amp;tab=associations#associations"`)

	second := getAuthenticated(t, handler, "/slurm/accounts?association_page=2&tab=associations", "zh")
	assertStatus(t, second, http.StatusOK)
	assertBodyContains(t, second, "user-100")
	assertBodyNotContains(t, second, "user-0</td>")
	assertBodyContains(t, second, `href="/slurm/accounts?association_page=1&amp;tab=associations#associations"`)
}

func TestAccountsPageFiltersAssociationsBeforePagination(t *testing.T) {
	associations := make([]cluster.Association, 101)
	for index := range associations {
		associations[index] = cluster.Association{
			ID: int64(index + 1), Cluster: "hpc", Account: "general", User: "user-" + strconv.Itoa(index),
		}
	}
	associations[100] = cluster.Association{ID: 101, Cluster: "hpc", Account: "research", User: "alice"}
	handler := newAccountingHandler(t, &stubAccountingProvider{
		directory: cluster.AccountDirectory{
			Accounts: []cluster.Account{{Name: "general", AssociationCount: 100}, {Name: "research", AssociationCount: 1}},
			Users:    []cluster.SlurmUser{{Name: "alice", AssociationCount: 1}},
		},
		associations: associations,
	})

	response := getAuthenticated(t, handler, "/slurm/accounts?tab=associations&association_account=research", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		"research", "alice", `class="account-section-count">1</span>`,
		`href="/slurm/accounts?tab=associations#associations"`, "Clear filter",
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "user-0</td>")
}

func TestAccountsPagePreservesAllFiltersAcrossAssociationPages(t *testing.T) {
	associations := make([]cluster.Association, 101)
	for index := range associations {
		associations[index] = cluster.Association{
			ID: int64(index + 1), Cluster: "hpc", Account: "research", User: "alice",
		}
	}
	handler := newAccountingHandler(t, &stubAccountingProvider{associations: associations})
	first := getAuthenticated(t, handler, "/slurm/accounts?association_account=research&association_user=alice&tab=associations", "en")
	assertStatus(t, first, http.StatusOK)
	assertBodyContains(t, first, `href="/slurm/accounts?association_account=research&amp;association_page=2&amp;association_user=alice&amp;tab=associations#associations"`)

	second := getAuthenticated(t, handler, "/slurm/accounts?association_account=research&association_page=2&association_user=alice&tab=associations", "en")
	assertStatus(t, second, http.StatusOK)
	assertBodyContains(t, second, `href="/slurm/accounts?association_account=research&amp;association_page=1&amp;association_user=alice&amp;tab=associations#associations"`)
}

func TestAccountsPageRejectsInvalidAssociationPagesBeforeProviderCalls(t *testing.T) {
	provider := &stubAccountingProvider{}
	handler := newAccountingHandler(t, provider)
	session := login(t, handler)
	for _, path := range []string{
		"/slurm/accounts?association_page=0",
		"/slurm/accounts?association_page=-1",
		"/slurm/accounts?association_page=01",
		"/slurm/accounts?association_page=abc",
		"/slurm/accounts?association_page=101",
		"/slurm/accounts?association_page=9223372036854775808",
		"/slurm/accounts?tab=overview",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(session)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusBadRequest)
	}
	if provider.associationCalls != 0 {
		t.Fatalf("association calls = %d, want 0", provider.associationCalls)
	}
}

func TestAccountsPageRejectsInvalidAssociationFiltersBeforeProviderCalls(t *testing.T) {
	provider := &stubAccountingProvider{}
	handler := newAccountingHandler(t, provider)
	session := login(t, handler)
	for _, path := range []string{
		"/slurm/accounts?association_account=" + strings.Repeat("a", maxAssociationFilterBytes+1),
		"/slurm/accounts?association_user=%00",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
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
	response := getAuthenticated(t, handler, "/slurm/accounts?association_page=2&tab=associations", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "No associations reported")
	assertBodyContains(t, response, `href="/slurm/accounts?association_page=1&amp;tab=associations#associations"`)
}

func TestAssociationsRouteRedirectsIntoAccountsPage(t *testing.T) {
	handler := newAccountingHandler(t, &stubAccountingProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/associations", nil)
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/slurm/accounts?tab=associations#associations")
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
