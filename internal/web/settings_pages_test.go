package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acdiost/openhpc-web/internal/platform"
)

func TestSettingsPageIsInSidebarAndRedactsSecretValues(t *testing.T) {
	store, err := platform.OpenSettingsStore(filepath.Join(t.TempDir(), "settings.db"), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD", "super-secret"); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store, SettingsDefaults: map[string]string{"OPENHPC_LDAP_URL": "ldaps://env.example:636"}})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	response := getAuthenticated(t, handler, "/settings", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, expected := range []string{
		"系统设置",
		"class=\"settings-hero\"",
		"class=\"settings-layout\"",
		"class=\"settings-aside\"",
		"class=\"settings-panel\"",
		"class=\"settings-switch\"",
		"class=\"secondary-button\"",
		"LDAP 地址",
		"SSH 登录节点",
		"ldaps://env.example:636",
		"已配置",
		`type="password"`,
	} {
		assertBodyContains(t, response, expected)
	}
	assertActiveNavigationLink(t, response.Body.String(), "/settings")
	assertBodyNotContains(t, response, "super-secret")
}

func TestSettingsPageRendersExamplePlaceholdersForEveryInput(t *testing.T) {
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	response := getAuthenticated(t, handler, "/settings", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, spec := range settingsSpecs {
		if spec.InputType == "checkbox" {
			continue
		}
		if spec.PlaceholderZH == "" || spec.PlaceholderEN == "" {
			t.Fatalf("%s is missing a placeholder", spec.Key)
		}
		assertBodyContains(t, response, `id="setting-`+spec.Key+`" type="`+spec.InputType+`" name="`+spec.Key+`" value="" placeholder="`+spec.PlaceholderZH+`"`)
	}
	english := getAuthenticated(t, handler, "/settings", "en")
	assertStatus(t, english, http.StatusOK)
	assertBodyContains(t, english, `placeholder="Enter Bind password"`)
}

func TestSettingsSaveRequiresCSRFAndPersistsWithoutLeakingSecret(t *testing.T) {
	store, err := platform.OpenSettingsStore(filepath.Join(t.TempDir(), "settings.db"), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	withoutCSRF := postFormWithCookies(handler, "/settings", url.Values{"OPENHPC_LDAP_URL": {"ldaps://ldap.example:636"}}, session)
	assertStatus(t, withoutCSRF, http.StatusForbidden)
	response := postProtectedForm(handler, "/settings", url.Values{
		"OPENHPC_LDAP_URL":           {"ldaps://ldap.example:636"},
		"OPENHPC_LDAP_BIND_PASSWORD": {"secret<&"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	value, found, err := store.Get(context.Background(), "OPENHPC_LDAP_URL")
	if err != nil || !found || value != "ldaps://ldap.example:636" {
		t.Fatalf("stored URL = %q, %v, %v", value, found, err)
	}
	auditResponse := getAuthenticated(t, handler, "/audit", "en")
	assertBodyContains(t, auditResponse, "settings.update")
	assertBodyNotContains(t, auditResponse, "secret<&")
}

func TestLDAPConnectivitySuccessRendersOneSuccessNotice(t *testing.T) {
	provider := &stubDirectoryProvider{}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DirectoryProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/settings/ldap-test", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusOK)
	if count := strings.Count(response.Body.String(), "LDAP 连通性测试成功。"); count != 1 {
		t.Fatalf("success notice count = %d, want 1", count)
	}
	assertBodyContains(t, response, `role="status">LDAP 连通性测试成功。`)
	assertBodyNotContains(t, response, `role="alert">LDAP 连通性测试成功。`)
	if provider.searchCalls != 1 || provider.lastQuery != "" {
		t.Fatalf("LDAP search calls/query = %d/%q, want 1/empty", provider.searchCalls, provider.lastQuery)
	}
}

func TestLDAPConnectivityFailureRendersOneErrorNotice(t *testing.T) {
	provider := &stubDirectoryProvider{err: errors.New("bind failed")}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DirectoryProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/settings/ldap-test", url.Values{}, session, csrf)
	assertStatus(t, response, http.StatusBadGateway)
	const notice = "LDAP 连通性测试失败，请检查地址、证书、Bind 凭据和基础 DN。"
	if count := strings.Count(response.Body.String(), notice); count != 1 {
		t.Fatalf("error notice count = %d, want 1", count)
	}
	assertBodyContains(t, response, `role="alert">`+notice)
	assertBodyNotContains(t, response, `role="status">`+notice)
}

func TestSettingsRejectsUnknownAndInvalidValuesWithoutWriting(t *testing.T) {
	store, err := platform.OpenSettingsStore(":memory:", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	for _, values := range []url.Values{
		{"OPENHPC_DATABASE_PATH": {"/tmp/unsafe"}},
		{"OPENHPC_LDAP_URL": {"ldap://insecure.example:389"}},
		{"OPENHPC_LDAP_MAX_RESULTS": {"9999"}},
	} {
		response := postProtectedForm(handler, "/settings", values, session, csrf)
		assertStatus(t, response, http.StatusBadRequest)
	}
	if _, found, err := store.Get(context.Background(), "OPENHPC_LDAP_URL"); err != nil || found {
		t.Fatalf("invalid setting persisted: found=%v err=%v", found, err)
	}
}

func TestSettingsRejectsIncompleteEnabledTerminalConfiguration(t *testing.T) {
	store, err := platform.OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	response := postProtectedForm(handler, "/settings", url.Values{
		"OPENHPC_TERMINAL_ENABLED": {"true"},
	}, session, csrf)
	assertStatus(t, response, http.StatusBadRequest)
	assertBodyContains(t, response, "SSH 终端配置无效")
	assertBodyContains(t, response, "host:port")
	if _, found, err := store.Get(context.Background(), "OPENHPC_TERMINAL_ENABLED"); err != nil || found {
		t.Fatalf("incomplete terminal configuration persisted: found=%v err=%v", found, err)
	}
}

func TestSettingsSavesCompleteTerminalConfiguration(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(directory, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte("login.example ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := platform.OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	response := postProtectedForm(handler, "/settings", url.Values{
		"OPENHPC_TERMINAL_ENABLED":         {"true", "false"},
		"OPENHPC_TERMINAL_SSH_ADDRESS":     {"login.example:22"},
		"OPENHPC_TERMINAL_SSH_KNOWN_HOSTS": {knownHosts},
		"OPENHPC_TERMINAL_TIMEOUT":         {"10s"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	for key, want := range map[string]string{
		"OPENHPC_TERMINAL_ENABLED":         "true",
		"OPENHPC_TERMINAL_SSH_ADDRESS":     "login.example:22",
		"OPENHPC_TERMINAL_SSH_KNOWN_HOSTS": knownHosts,
		"OPENHPC_TERMINAL_TIMEOUT":         "10s",
	} {
		got, found, err := store.Get(context.Background(), key)
		if err != nil || !found || got != want {
			t.Errorf("stored %s = %q, found=%v, err=%v; want %q", key, got, found, err, want)
		}
	}
}

func TestSettingsSecretSaveExplainsMissingEncryptionKey(t *testing.T) {
	store, err := platform.OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, SettingsStore: store})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	session, csrf := loginWithCSRF(t, handler)
	response := postProtectedForm(handler, "/settings", url.Values{"OPENHPC_LDAP_BIND_PASSWORD": {"secret"}}, session, csrf)
	assertStatus(t, response, http.StatusServiceUnavailable)
	assertBodyContains(t, response, "OPENHPC_SETTINGS_KEY")
	assertBodyNotContains(t, response, "secret")
}

func TestSettingsPageRequiresAuthentication(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/settings", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
}
