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

	confirmation := postProtectedForm(handler, "/platform/users/status", url.Values{"username": {"alice"}, "enabled": {"false"}}, adminSession, csrf)
	assertStatus(t, confirmation, http.StatusSeeOther)
	assertHeader(t, confirmation, "Location", "/platform/users?confirm=disable&username=alice")
	confirmationPage := getAuthenticated(t, handler, "/platform/users?confirm=disable&username=alice", "en")
	assertStatus(t, confirmationPage, http.StatusOK)
	assertBodyContains(t, confirmationPage, "Confirm account disable")
	assertBodyContains(t, confirmationPage, "Disable alice? Active sessions will end immediately.")
	assertBodyContains(t, confirmationPage, `name="confirmed" value="true"`)
	stillEnabled, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found || !stillEnabled.Enabled {
		t.Fatalf("unconfirmed disable changed user: %#v, found=%v, err=%v", stillEnabled, found, err)
	}

	disable := postProtectedForm(handler, "/platform/users/status", url.Values{"username": {"alice"}, "enabled": {"false"}, "confirmed": {"true"}}, adminSession, csrf)
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

func TestPlatformUsersPageShowsAccountSummaryAndManagementGuidance(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for _, user := range []platform.PlatformUser{
		{Username: "alice", PasswordHash: "hash", Role: platform.RoleUser, Enabled: true},
		{Username: "suspended", PasswordHash: "hash", Role: platform.RoleUser, Enabled: false},
	} {
		if err := store.Create(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)

	response := getAuthenticated(t, handler, "/platform/users", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		`class="platform-user-summary"`, `<span>Total accounts</span><strong>3</strong>`,
		`<span>Active</span><strong>2</strong>`, `<span>Disabled</span><strong>1</strong>`,
		`data-platform-user-create`, `aria-haspopup="dialog"`, `id="platform-user-create-modal"`,
		`aria-labelledby="platform-user-create-title"`, `data-platform-user-create-close`, `href="/platform/users?create=1"`,
		`data-platform-user-status="enabled"`, `data-platform-user-status="disabled"`,
		"Use 1-64 letters, digits, dots, underscores, or hyphens.", "Use 12-72 bytes.",
		`aria-label="Disable alice"`, `data-confirm="Disable alice? Active sessions will end immediately."`,
		"Disabling an account immediately ends its active sessions.", `class="data-table platform-user-table`,
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, `minlength="12"`)
	assertBodyNotContains(t, response, `maxlength="72"`)
	createPage := getAuthenticated(t, handler, "/platform/users?create=1", "en")
	assertStatus(t, createPage, http.StatusOK)
	assertBodyContains(t, createPage, `data-platform-user-create-open`)

	chineseResponse := getAuthenticated(t, handler, "/platform/users", "zh")
	assertStatus(t, chineseResponse, http.StatusOK)
	for _, value := range []string{"账号总数", "已启用", "已停用", `data-confirm="停用 alice？其活跃会话将立即结束。"`} {
		assertBodyContains(t, chineseResponse, value)
	}
}
