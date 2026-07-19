package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/acdiost/openhpc-web/internal/directory"
	"github.com/acdiost/openhpc-web/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

const testPlatformLDAPPassphrase = "ldap user password"

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

func TestPlatformUserCanCreateLinkedLDAPUser(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &stubLDAPUserProvisioner{}
	if err := store.Create(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: "hash", Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store, DirectoryProvider: provisioner})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)

	page := getAuthenticated(t, handler, "/platform/users?ldap_user=alice", "en")
	assertStatus(t, page, http.StatusOK)
	assertBodyContains(t, page, `href="/platform/users?ldap_user=alice"`)
	assertBodyContains(t, page, `id="platform-ldap-create-modal"`)
	assertBodyContains(t, page, `data-platform-ldap-create-open`)
	assertBodyContains(t, page, `name="username" value="alice"`)

	session, csrf := loginWithCSRF(t, handler)
	created := postProtectedForm(handler, "/platform/users/ldap", url.Values{
		"username":       {"alice"},
		"uid_number":     {"1001"},
		"gid_number":     {"1001"},
		"home_directory": {"/home/alice"},
		"login_shell":    {"/bin/bash"},
		"password":       {testPlatformLDAPPassphrase},
	}, session, csrf)
	assertStatus(t, created, http.StatusSeeOther)
	assertHeader(t, created, "Location", "/platform/users?result=ldap-created")
	if provisioner.calls != 1 {
		t.Fatalf("LDAP create calls = %d, want 1", provisioner.calls)
	}
	want := directory.UserCreateRequest{UID: "alice", UIDNumber: 1001, GIDNumber: 1001, HomeDirectory: "/home/alice", LoginShell: "/bin/bash", Password: testPlatformLDAPPassphrase}
	if provisioner.request != want {
		t.Errorf("LDAP request = %#v, want %#v", provisioner.request, want)
	}
	assertAuditOutcome(t, handler.(*Handler).audit, "ldap.user.create", "success")

	unknown := postProtectedForm(handler, "/platform/users/ldap", url.Values{
		"username": {"missing"}, "uid_number": {"1002"}, "gid_number": {"1002"},
		"home_directory": {"/home/missing"}, "login_shell": {"/bin/bash"}, "password": {testPlatformLDAPPassphrase},
	}, session, csrf)
	assertStatus(t, unknown, http.StatusSeeOther)
	assertHeader(t, unknown, "Location", "/platform/users?result=ldap-invalid")
	if provisioner.calls != 1 {
		t.Fatalf("LDAP create calls after invalid user = %d, want 1", provisioner.calls)
	}
	assertAuditOutcome(t, handler.(*Handler).audit, "ldap.user.create", "denied")

	invalid := postProtectedForm(handler, "/platform/users/ldap", url.Values{
		"username": {"alice"}, "uid_number": {"bad"}, "gid_number": {"1001"},
		"home_directory": {"/home/alice"}, "login_shell": {"/bin/bash"}, "password": {testPlatformLDAPPassphrase},
	}, session, csrf)
	assertStatus(t, invalid, http.StatusSeeOther)
	assertHeader(t, invalid, "Location", "/platform/users?result=ldap-invalid")
	assertAuditOutcome(t, handler.(*Handler).audit, "ldap.user.create", "invalid_request")

	provisioner.err = errors.New("bind cn=secret password=hunter2")
	failure := postProtectedForm(handler, "/platform/users/ldap", url.Values{
		"username": {"alice"}, "uid_number": {"1003"}, "gid_number": {"1001"},
		"home_directory": {"/home/alice"}, "login_shell": {"/bin/bash"}, "password": {testPlatformLDAPPassphrase},
	}, session, csrf)
	assertStatus(t, failure, http.StatusSeeOther)
	assertHeader(t, failure, "Location", "/platform/users?result=ldap-error")
	assertAuditOutcome(t, handler.(*Handler).audit, "ldap.user.create", "failure")
	errorPage := getAuthenticated(t, handler, "/platform/users?result=ldap-error", "en")
	assertBodyNotContains(t, errorPage, "cn=secret")
	assertBodyNotContains(t, errorPage, "hunter2")

	provisioner.err = nil
	if err := store.SetEnabled(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	disabled := postProtectedForm(handler, "/platform/users/ldap", url.Values{
		"username": {"alice"}, "uid_number": {"1004"}, "gid_number": {"1001"},
		"home_directory": {"/home/alice"}, "login_shell": {"/bin/bash"}, "password": {testPlatformLDAPPassphrase},
	}, session, csrf)
	assertStatus(t, disabled, http.StatusSeeOther)
	assertHeader(t, disabled, "Location", "/platform/users?result=ldap-invalid")
	assertAuditOutcome(t, handler.(*Handler).audit, "ldap.user.create", "denied")
}

func TestPlatformUserLDAPCreateDialogLoadsSelectableGroups(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: "hash", Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	provisioner := &stubLDAPUserProvisioner{page: directory.Page{Groups: []directory.Group{
		{Name: "root", GIDNumber: 0},
		{Name: "research", GIDNumber: 2001},
		{Name: "dev <ops>", GIDNumber: 2002},
	}}}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store, DirectoryProvider: provisioner})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)

	page := getAuthenticated(t, handler, "/platform/users?ldap_user=alice", "en")
	assertStatus(t, page, http.StatusOK)
	assertBodyContains(t, page, `id="ldap-gid-number" name="gid_number" required`)
	assertBodyContains(t, page, `<option value="2001">research (2001)</option>`)
	assertBodyContains(t, page, `<option value="2002">dev &lt;ops&gt; (2002)</option>`)
	assertBodyNotContains(t, page, `<option value="0">root (0)</option>`)
	if provisioner.searchCalls != 1 {
		t.Fatalf("LDAP group search calls = %d, want 1", provisioner.searchCalls)
	}
}

func TestPlatformUsersShowInsecureLDAPProvisioningEntry(t *testing.T) {
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: "hash", Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	provisioner := &insecureLDAPUserProvisioner{}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store, DirectoryProvider: provisioner})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)

	page := getAuthenticated(t, handler, "/platform/users?ldap_user=alice", "en")
	assertStatus(t, page, http.StatusOK)
	assertBodyContains(t, page, `href="/platform/users?ldap_user=alice"`)
	assertBodyContains(t, page, `id="platform-ldap-create-modal"`)

	if provisioner.calls != 0 {
		t.Fatalf("LDAP create calls = %d, want 0", provisioner.calls)
	}
}

type stubLDAPUserProvisioner struct {
	calls       int
	request     directory.UserCreateRequest
	err         error
	page        directory.Page
	searchCalls int
}

func (p *stubLDAPUserProvisioner) CreateUser(_ context.Context, request directory.UserCreateRequest) error {
	p.calls++
	p.request = request
	return p.err
}

func (p *stubLDAPUserProvisioner) Search(context.Context, string) (directory.Page, error) {
	p.searchCalls++
	return p.page, nil
}

func (p *stubLDAPUserProvisioner) User(context.Context, string) (directory.User, bool, error) {
	return directory.User{}, false, nil
}

func (p *stubLDAPUserProvisioner) Group(context.Context, string) (directory.Group, bool, error) {
	return directory.Group{}, false, nil
}

type insecureLDAPUserProvisioner struct{ stubLDAPUserProvisioner }

func (*insecureLDAPUserProvisioner) UserProvisioningAvailable() bool { return true }
func (*insecureLDAPUserProvisioner) UserProvisioningSupported() bool { return true }
