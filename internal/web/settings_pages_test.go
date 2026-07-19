package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
		`href="/settings" class="nav-item sidebar-settings active"`,
		"系统设置",
		"class=\"settings-hero\"",
		"class=\"settings-layout\"",
		"class=\"settings-aside\"",
		"class=\"settings-panel\"",
		"class=\"settings-switch\"",
		"class=\"secondary-button\"",
		"LDAP 地址",
		"ldaps://env.example:636",
		"已配置",
		`type="password"`,
	} {
		assertBodyContains(t, response, expected)
	}
	assertBodyNotContains(t, response, "super-secret")
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
