package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/acdiost/openhpc-web/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

func TestPlatformUserLifecycleCreatesWithoutOverwriteAndRevokesDisabledSessions(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)

	adminSession, csrf := loginWithCSRF(t, handler)
	create := postProtectedForm(handler, "/platform/users", url.Values{
		"username": {"alice"},
		"password": {"ordinary user password"},
		"role":     {"user"},
	}, adminSession, csrf)
	assertStatus(t, create, http.StatusSeeOther)
	assertHeader(t, create, "Location", "/platform/users?result=created")

	created, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found || bcrypt.CompareHashAndPassword([]byte(created.PasswordHash), []byte("ordinary user password")) != nil {
		t.Fatalf("created user = %#v, found=%v, err=%v", created, found, err)
	}

	duplicate := postProtectedForm(handler, "/platform/users", url.Values{
		"username": {"alice"},
		"password": {"replacement password"},
		"role":     {"admin"},
	}, adminSession, csrf)
	assertStatus(t, duplicate, http.StatusSeeOther)
	assertHeader(t, duplicate, "Location", "/platform/users?result=duplicate")

	unchanged, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found || unchanged.Role != platform.RoleUser || bcrypt.CompareHashAndPassword([]byte(unchanged.PasswordHash), []byte("ordinary user password")) != nil {
		t.Fatalf("duplicate creation changed user: %#v, found=%v, err=%v", unchanged, found, err)
	}

	resultPage := getAuthenticated(t, handler, "/platform/users?result=created", "zh")
	assertStatus(t, resultPage, http.StatusOK)
	assertBodyContains(t, resultPage, "平台用户已创建。")

	aliceLogin := postForm(handler, "/login", url.Values{"username": {"alice"}, "password": {"ordinary user password"}}, nil)
	assertStatus(t, aliceLogin, http.StatusSeeOther)
	aliceSession := findCookie(t, aliceLogin.Result().Cookies(), sessionCookie)

	disable := postProtectedForm(handler, "/platform/users/status", url.Values{"username": {"alice"}, "enabled": {"false"}}, adminSession, csrf)
	assertStatus(t, disable, http.StatusSeeOther)
	assertHeader(t, disable, "Location", "/platform/users?result=updated")

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(aliceSession)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/login?next=%2Fdashboard")
}

func TestCreatePlatformUserRejectsPasswordBeyondBcryptLimit(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/platform/users", url.Values{
		"username": {"alice"},
		"password": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"role":     {"user"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/platform/users?result=invalid")
}
