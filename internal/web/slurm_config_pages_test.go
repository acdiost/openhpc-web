package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/slurmconfig"
)

func TestSlurmConfigPageListsAndReadsEscapedRedactedFile(t *testing.T) {
	provider := &stubSlurmConfigProvider{
		entries: []slurmconfig.Entry{{Name: "gres.conf", Size: 12}, {Name: "slurm.conf", Size: 40}},
		files:   map[string]slurmconfig.File{"slurm.conf": {Name: "slurm.conf", Size: 40, Content: "ClusterName=<cluster>\nAccountingStoragePass=REDACTED\n"}},
	}
	handler := newSlurmConfigHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/config?file=slurm.conf", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"Slurm 配置", "slurm.conf", "gres.conf", "ClusterName=&lt;cluster&gt;", "AccountingStoragePass=REDACTED", "只读"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "super-secret")
	if provider.readCalls != 1 || provider.lastRead != "slurm.conf" {
		t.Fatalf("read calls/name = %d/%q", provider.readCalls, provider.lastRead)
	}
}

func TestSlurmConfigPageRejectsInvalidFileBeforeProviderRead(t *testing.T) {
	provider := &stubSlurmConfigProvider{}
	handler := newSlurmConfigHandler(t, provider)
	for _, file := range []string{"../passwd", "/etc/passwd", "nested/slurm.conf", "bad name"} {
		response := getAuthenticated(t, handler, "/slurm/config?file="+url.QueryEscape(file), "en")
		assertStatus(t, response, http.StatusBadRequest)
	}
	if provider.readCalls != 0 {
		t.Fatalf("read calls = %d, want none", provider.readCalls)
	}
}

func TestSlurmConfigPageDegradesWithoutProviderError(t *testing.T) {
	provider := &stubSlurmConfigProvider{err: errors.New("/etc/slurm/private.conf password=secret")}
	response := getAuthenticated(t, newSlurmConfigHandler(t, provider), "/slurm/config", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Slurm configuration is temporarily unavailable")
	assertBodyNotContains(t, response, "/etc/slurm/private.conf")
	assertBodyNotContains(t, response, "password=secret")
}

func TestSlurmConfigPageRecordsAuditForSuccessfulRead(t *testing.T) {
	provider := &stubSlurmConfigProvider{
		entries: []slurmconfig.Entry{{Name: "slurm.conf", Size: 40}},
		files:   map[string]slurmconfig.File{"slurm.conf": {Name: "slurm.conf", Size: 40, Content: "ClusterName=cluster\n"}},
	}
	handler := newSlurmConfigHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/config?file=slurm.conf", "en")
	assertStatus(t, response, http.StatusOK)
	assertAuditOutcome(t, handler.(*Handler).audit, "slurm.config.read", "success")
}

func TestSlurmConfigPageRecordsAuditForInvalidRequest(t *testing.T) {
	handler := newSlurmConfigHandler(t, &stubSlurmConfigProvider{})
	response := getAuthenticated(t, handler, "/slurm/config?file=../passwd", "en")
	assertStatus(t, response, http.StatusBadRequest)
	assertAuditOutcome(t, handler.(*Handler).audit, "slurm.config.read", "invalid_request")
}

func TestSlurmConfigPageRequiresAuthentication(t *testing.T) {
	handler := newSlurmConfigHandler(t, &stubSlurmConfigProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/config", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
}

func newSlurmConfigHandler(t *testing.T, provider slurmconfig.Provider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SlurmConfigProvider: provider})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

type stubSlurmConfigProvider struct {
	entries   []slurmconfig.Entry
	files     map[string]slurmconfig.File
	err       error
	readCalls int
	lastRead  string
}

func (p *stubSlurmConfigProvider) List(context.Context) ([]slurmconfig.Entry, error) {
	return append([]slurmconfig.Entry(nil), p.entries...), p.err
}

func (p *stubSlurmConfigProvider) Read(_ context.Context, name string) (slurmconfig.File, error) {
	p.readCalls++
	p.lastRead = name
	if p.err != nil {
		return slurmconfig.File{}, p.err
	}
	file, ok := p.files[name]
	if !ok {
		return slurmconfig.File{}, errors.New("not found")
	}
	return file, nil
}

func assertAuditOutcome(t *testing.T, store *platform.AuditStore, action, outcome string) {
	t.Helper()
	page, err := store.List(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("audit.List() error = %v", err)
	}
	for _, event := range page.Events {
		if event.Action == action {
			if event.Outcome != outcome {
				t.Fatalf("audit outcome = %q, want %q", event.Outcome, outcome)
			}
			return
		}
	}
	t.Fatalf("audit action %q not found in %v", action, page.Events)
}
