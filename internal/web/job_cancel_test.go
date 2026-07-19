package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

func TestSlurmJobCancelAllowsPlatformAdminToCancelCrossUserJob(t *testing.T) {
	canceler := &stubJobCanceler{}
	handler := newJobCancelHandler(t, "operator", nil, &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: "alice"}}}, canceler)
	login := postForm(handler, "/login", url.Values{"username": {"operator"}, "password": {testPassword}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)
	csrf := findCookie(t, login.Result().Cookies(), csrfCookie)

	response := postProtectedForm(handler, "/slurm/jobs/32943/cancel", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	if location := response.Header().Get("Location"); location != "/slurm/jobs" {
		t.Errorf("Location = %q, want /slurm/jobs", location)
	}
	if canceler.calls != 1 || canceler.lastJobID != 32943 {
		t.Errorf("canceler calls = %d (%d)", canceler.calls, canceler.lastJobID)
	}
}

func TestSlurmJobCancelDeniesOtherUsers(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("ordinary user password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: string(passwordHash), Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	canceler := &stubJobCanceler{}
	handler := newJobCancelHandler(t, testUsername, store, &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: "bob"}}}, canceler)
	login := postForm(handler, "/login", url.Values{"username": {"alice"}, "password": {"ordinary user password"}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)
	csrf := findCookie(t, login.Result().Cookies(), csrfCookie)

	response := postProtectedForm(handler, "/slurm/jobs/32943/cancel", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusNotFound)
	if canceler.calls != 0 {
		t.Errorf("canceler calls = %d, want 0", canceler.calls)
	}
}

func TestSlurmJobCancelAllowsSubmittingUser(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("ordinary user password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: string(passwordHash), Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	canceler := &stubJobCanceler{}
	handler := newJobCancelHandler(t, testUsername, store, &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: "alice"}}}, canceler)
	login := postForm(handler, "/login", url.Values{"username": {"alice"}, "password": {"ordinary user password"}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)
	csrf := findCookie(t, login.Result().Cookies(), csrfCookie)

	response := postProtectedForm(handler, "/slurm/jobs/32943/cancel", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	if canceler.calls != 1 || canceler.lastJobID != 32943 {
		t.Errorf("canceler calls = %d (%d)", canceler.calls, canceler.lastJobID)
	}
}

func TestSlurmJobCancelDoesNotGrantCrossUserAccessToOrdinaryRootUser(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("ordinary root password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), platform.PlatformUser{Username: "root", PasswordHash: string(passwordHash), Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	canceler := &stubJobCanceler{}
	handler := newJobCancelHandler(t, "operator", store, &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: "alice"}}}, canceler)
	login := postForm(handler, "/login", url.Values{"username": {"root"}, "password": {"ordinary root password"}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)
	csrf := findCookie(t, login.Result().Cookies(), csrfCookie)

	response := postProtectedForm(handler, "/slurm/jobs/32943/cancel", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusNotFound)
	if canceler.calls != 0 {
		t.Errorf("canceler calls = %d, want 0", canceler.calls)
	}
}

func TestSlurmJobCancelRejectsInvalidAndUnavailableRequests(t *testing.T) {
	for _, test := range []struct {
		name     string
		path     string
		jobs     *stubJobProvider
		canceler cluster.JobCanceler
		status   int
	}{
		{name: "invalid ID", path: "/slurm/jobs/not-a-number/cancel", jobs: &stubJobProvider{}, canceler: &stubJobCanceler{}, status: http.StatusBadRequest},
		{name: "missing job", path: "/slurm/jobs/32943/cancel", jobs: &stubJobProvider{}, canceler: &stubJobCanceler{}, status: http.StatusNotFound},
		{name: "job lookup failed", path: "/slurm/jobs/32943/cancel", jobs: &stubJobProvider{err: errors.New("secret lookup")}, canceler: &stubJobCanceler{}, status: http.StatusServiceUnavailable},
		{name: "cancel failed", path: "/slurm/jobs/32943/cancel", jobs: &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: testUsername}}}, canceler: &stubJobCanceler{err: errors.New("secret scancel")}, status: http.StatusServiceUnavailable},
		{name: "canceler disabled", path: "/slurm/jobs/32943/cancel", jobs: &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: testUsername}}}, status: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newJobCancelHandler(t, testUsername, nil, test.jobs, test.canceler)
			session, csrf := loginWithCSRF(t, handler)
			response := postProtectedForm(handler, test.path, url.Values{}, session, csrf)
			assertStatus(t, response, test.status)
			assertBodyNotContains(t, response, "secret")
		})
	}
}

func TestSlurmJobCancelRequiresCSRF(t *testing.T) {
	canceler := &stubJobCanceler{}
	handler := newJobCancelHandler(t, testUsername, nil, &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: testUsername}}}, canceler)
	session := login(t, handler)

	response := postForm(handler, "/slurm/jobs/32943/cancel", url.Values{}, session)
	assertStatus(t, response, http.StatusForbidden)
	if canceler.calls != 0 {
		t.Errorf("canceler calls = %d, want 0", canceler.calls)
	}
}

func newJobCancelHandler(t *testing.T, adminUsername string, users *platform.UserStore, jobs cluster.JobProvider, canceler cluster.JobCanceler) http.Handler {
	t.Helper()
	handler, err := New(Config{
		AdminUsername: adminUsername, AdminPassword: testPassword, PlatformUsers: users,
		JobProvider: jobs, JobCanceler: canceler,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

type stubJobCanceler struct {
	err       error
	calls     int
	lastJobID int64
}

func (c *stubJobCanceler) CancelJob(_ context.Context, id int64) error {
	c.calls++
	c.lastJobID = id
	return c.err
}
