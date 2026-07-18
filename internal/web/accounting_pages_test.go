package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestAccountsPageShowsAccountsAndUsersInApplicationShell(t *testing.T) {
	provider := &stubAccountingProvider{directory: cluster.AccountDirectory{
		Accounts: []cluster.Account{{Name: "jfzx", Description: "<research>", Organization: "lab", AssociationCount: 2}},
		Users:    []cluster.SlurmUser{{Name: "alice", AdministratorLevel: "None", DefaultAccount: "jfzx", AssociationCount: 1}},
	}}
	handler := newAccountingHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/accounts", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{`class="app-shell"`, "账户与用户", "jfzx", "&lt;research&gt;", "alice", "默认账户"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "<research>")
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
	for _, path := range []string{"/slurm/accounts", "/slurm/qos"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusFound)
		assertHeader(t, response, "Location", "/login?next="+url.QueryEscape(path))
	}
}

func newAccountingHandler(t *testing.T, provider cluster.AccountingProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, AccountingProvider: provider})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

type stubAccountingProvider struct {
	directory cluster.AccountDirectory
	qos       []cluster.QoS
	err       error
}

func (p *stubAccountingProvider) AccountDirectory(context.Context) (cluster.AccountDirectory, error) {
	return p.directory, p.err
}
func (p *stubAccountingProvider) QoS(context.Context) ([]cluster.QoS, error) {
	return append([]cluster.QoS(nil), p.qos...), p.err
}
