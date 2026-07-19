package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/platform"
	"github.com/openhpc-web/openhpc-web/internal/slurmconfig"
	"github.com/openhpc-web/openhpc-web/internal/web"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("OPENHPC_TEST_VALUE", "configured")
	if got := envOr("OPENHPC_TEST_VALUE", "fallback"); got != "configured" {
		t.Errorf("envOr() = %q, want configured value", got)
	}

	t.Setenv("OPENHPC_TEST_VALUE", "")
	if got := envOr("OPENHPC_TEST_VALUE", "fallback"); got != "fallback" {
		t.Errorf("envOr() = %q, want fallback", got)
	}
}

func TestSettingsOverrideTakesPrecedenceOverEnvironment(t *testing.T) {
	t.Setenv("OPENHPC_SLURM_ENABLED", "true")
	t.Setenv("OPENHPC_SLURM_BIN_DIR", "/env/bin")
	t.Setenv("OPENHPC_SLURM_TIMEOUT", "3s")
	t.Setenv("OPENHPC_SLURM_MAX_OUTPUT", "2097152")
	t.Setenv("OPENHPC_SLURM_CACHE_TTL", "10s")
	store, err := platform.OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Set(context.Background(), "OPENHPC_SLURM_BIN_DIR", "/db/bin"); err != nil {
		t.Fatal(err)
	}
	_, config, err := parseSlurmConfigFromStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if config.BinaryDir != "/db/bin" {
		t.Errorf("BinaryDir = %q", config.BinaryDir)
	}
}

func TestParseSettingsKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("OPENHPC_SETTINGS_KEY", base64.StdEncoding.EncodeToString(key))
	parsed, err := parseSettingsKey()
	if err != nil || string(parsed) != string(key) {
		t.Fatalf("parseSettingsKey() = %x, %v", parsed, err)
	}
	t.Setenv("OPENHPC_SETTINGS_KEY", "invalid")
	if _, err := parseSettingsKey(); err == nil {
		t.Fatal("parseSettingsKey(invalid) error = nil")
	}
}

func TestRuntimeUserWarningAllowsRootWithExplicitRiskWarning(t *testing.T) {
	warning := runtimeUserWarning(0)
	for _, required := range []string{"WARNING", "root", "does not drop privileges", "owner/UID", "operating-system permissions"} {
		if !strings.Contains(warning, required) {
			t.Errorf("runtimeUserWarning(0) = %q, want %q", warning, required)
		}
	}
}

func TestRuntimeUserWarningIsEmptyForNonRoot(t *testing.T) {
	for _, euid := range []int{1, 1000, 65534} {
		if warning := runtimeUserWarning(euid); warning != "" {
			t.Errorf("runtimeUserWarning(%d) = %q, want empty", euid, warning)
		}
	}
}

func TestSplitList(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "empty", value: "", want: nil},
		{name: "whitespace", value: "  ", want: nil},
		{name: "single", value: "127.0.0.0/8", want: []string{"127.0.0.0/8"}},
		{name: "trims and omits empty entries", value: " 127.0.0.0/8, ,10.0.0.0/8 ", want: []string{"127.0.0.0/8", "10.0.0.0/8"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := splitList(test.value); !reflect.DeepEqual(got, test.want) {
				t.Errorf("splitList(%q) = %#v, want %#v", test.value, got, test.want)
			}
		})
	}
}

func TestIsLoopbackAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "127.0.0.1:8080", want: true},
		{address: "[::1]:8080", want: true},
		{address: "0.0.0.0:8080", want: false},
		{address: "192.0.2.10:8080", want: false},
		{address: "localhost:8080", want: false},
		{address: "missing-port", want: false},
	}

	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := isLoopbackAddress(test.address); got != test.want {
				t.Errorf("isLoopbackAddress(%q) = %v, want %v", test.address, got, test.want)
			}
		})
	}
}

func TestValidateDeploymentConfig(t *testing.T) {
	tests := []struct {
		name              string
		address           string
		secureCookies     bool
		trustedProxyCIDRs []string
		wantError         bool
	}{
		{name: "public listener without secure cookies", address: "0.0.0.0:8080", wantError: true},
		{name: "public listener remains forbidden with TLS proxy settings", address: "192.0.2.10:8080", secureCookies: true, trustedProxyCIDRs: []string{"127.0.0.0/8"}, wantError: true},
		{name: "secure cookies require trusted proxy", address: "127.0.0.1:8080", secureCookies: true, wantError: true},
		{name: "loopback development", address: "127.0.0.1:8080", wantError: false},
		{name: "loopback behind trusted TLS proxy", address: "[::1]:8080", secureCookies: true, trustedProxyCIDRs: []string{"::1/128"}, wantError: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDeploymentConfig(test.address, test.secureCookies, test.trustedProxyCIDRs)
			if test.wantError && err == nil {
				t.Fatal("validateDeploymentConfig() error = nil, want error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("validateDeploymentConfig() error = %v", err)
			}
		})
	}
}

func TestPrepareStateDirectoryCreatesDatabaseParentWithOwnerOnlyPermissions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nested", "state", "openhpc.db")
	directory := filepath.Dir(databasePath)

	warnings, err := prepareStateDirectory(databasePath)
	if err != nil {
		t.Fatalf("prepareStateDirectory() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("prepareStateDirectory() warnings = %q, want none", warnings)
	}

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", directory, err)
	}
	if !info.IsDir() {
		t.Fatalf("database parent %q is not a directory", directory)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("database parent permissions = %04o, want %04o", got, want)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Errorf("prepareStateDirectory() created database file; Stat error = %v", err)
	}
}

func TestPrepareStateDirectoryWarnsForPermissiveParent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	warnings, err := prepareStateDirectory(filepath.Join(directory, "openhpc.db"))
	if err != nil {
		t.Fatalf("prepareStateDirectory() error = %v, want warning only", err)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "WARNING") {
		t.Fatalf("prepareStateDirectory() warnings = %q, want permission warning", warnings)
	}
}

func TestStateDirectoryWarningsReportOwnerMismatchWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	warnings := stateDirectoryWarnings(info, os.Geteuid()+1)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "owner") {
		t.Fatalf("stateDirectoryWarnings() = %q, want owner warning", warnings)
	}
}

func TestPrepareStateDirectoryAllowsInMemoryDatabase(t *testing.T) {
	warnings, err := prepareStateDirectory(":memory:")
	if err != nil {
		t.Fatalf("prepareStateDirectory(:memory:) error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("prepareStateDirectory(:memory:) warnings = %q", warnings)
	}
}

func TestParseSlurmConfigFromEnvDisabledUnlessExplicitlyTrue(t *testing.T) {
	for _, enabledValue := range []string{"", "false", "TRUE", "1"} {
		t.Run(enabledValue, func(t *testing.T) {
			setSlurmEnvironment(t, enabledValue, "/custom/bin", "invalid-duration", "invalid-integer")

			enabled, _, err := parseSlurmConfigFromEnv()
			if err != nil {
				t.Fatalf("parseSlurmConfigFromEnv() error = %v while disabled", err)
			}
			if enabled {
				t.Errorf("enabled = true for OPENHPC_SLURM_ENABLED=%q", enabledValue)
			}
		})
	}
}

func TestParseSlurmConfigFromEnvDefaults(t *testing.T) {
	setSlurmEnvironment(t, "true", "", "", "")

	enabled, config, err := parseSlurmConfigFromEnv()
	if err != nil {
		t.Fatalf("parseSlurmConfigFromEnv() error = %v", err)
	}
	if !enabled {
		t.Fatal("enabled = false, want true")
	}
	if config.BinaryDir != "/usr/local/bin" {
		t.Errorf("BinaryDir = %q, want /usr/local/bin", config.BinaryDir)
	}
	if config.Timeout != 3*time.Second {
		t.Errorf("Timeout = %v, want 3s", config.Timeout)
	}
	if config.MaxOutputBytes != 2_097_152 {
		t.Errorf("MaxOutputBytes = %d, want 2097152", config.MaxOutputBytes)
	}
	if config.CacheTTL != 10*time.Second {
		t.Errorf("CacheTTL = %v, want 10s", config.CacheTTL)
	}
	if config.Runner != nil {
		t.Error("Runner must be nil so slurm.New selects the production runner")
	}
}

func TestParseSlurmConfigFromEnvCustomValues(t *testing.T) {
	setSlurmEnvironment(t, "true", "/opt/slurm/bin", "750ms", "65536")

	enabled, config, err := parseSlurmConfigFromEnv()
	if err != nil {
		t.Fatalf("parseSlurmConfigFromEnv() error = %v", err)
	}
	if !enabled {
		t.Fatal("enabled = false, want true")
	}
	if config.BinaryDir != "/opt/slurm/bin" || config.Timeout != 750*time.Millisecond || config.MaxOutputBytes != 65_536 {
		t.Errorf("config = %#v, want custom Slurm values", config)
	}
}

func TestParseSlurmConfigFromEnvReportsInvalidValues(t *testing.T) {
	tests := []struct {
		name       string
		timeout    string
		maxOutput  string
		configName string
	}{
		{name: "duration", timeout: "three seconds", maxOutput: "1024", configName: "OPENHPC_SLURM_TIMEOUT"},
		{name: "integer", timeout: "3s", maxOutput: "sixty-four-kib", configName: "OPENHPC_SLURM_MAX_OUTPUT"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setSlurmEnvironment(t, "true", "/opt/slurm/bin", test.timeout, test.maxOutput)

			_, _, err := parseSlurmConfigFromEnv()
			if err == nil {
				t.Fatal("parseSlurmConfigFromEnv() error = nil, want parse error")
			}
			if !strings.Contains(err.Error(), test.configName) {
				t.Errorf("error = %q, want configuration name %q", err, test.configName)
			}
		})
	}
}

func TestBuildHandlerClosesSlurmConfigProviderOnError(t *testing.T) {
	previous := newWebHandler
	defer func() { newWebHandler = previous }()
	newWebHandler = func(web.Config) (http.Handler, error) {
		return nil, errors.New("boom")
	}

	provider := &closingSlurmConfigProvider{}
	_, err := buildHandler(web.Config{}, provider)
	if err == nil {
		t.Fatal("buildHandler() error = nil, want error")
	}
	if provider.closed != 1 {
		t.Fatalf("provider closed = %d, want 1", provider.closed)
	}
}

type closingSlurmConfigProvider struct {
	closed int
}

func (p *closingSlurmConfigProvider) List(context.Context) ([]slurmconfig.Entry, error) {
	return nil, nil
}

func (p *closingSlurmConfigProvider) Read(context.Context, string) (slurmconfig.File, error) {
	return slurmconfig.File{}, nil
}

func (p *closingSlurmConfigProvider) Close() error {
	p.closed++
	return nil
}

func setSlurmEnvironment(t *testing.T, enabled, binaryDir, timeout, maxOutput string) {
	t.Helper()
	t.Setenv("OPENHPC_SLURM_ENABLED", enabled)
	t.Setenv("OPENHPC_SLURM_BIN_DIR", binaryDir)
	t.Setenv("OPENHPC_SLURM_TIMEOUT", timeout)
	t.Setenv("OPENHPC_SLURM_MAX_OUTPUT", maxOutput)
}

func TestParseLDAPConfigFromEnvDisabledUnlessExplicitlyTrue(t *testing.T) {
	for _, enabled := range []string{"", "false", "TRUE", "1"} {
		t.Run(enabled, func(t *testing.T) {
			setLDAPEnvironment(t, enabled, "not a URL", "", "invalid", "invalid")
			configured, _, err := parseLDAPConfigFromEnv()
			if err != nil || configured {
				t.Fatalf("parseLDAPConfigFromEnv() = (%v, %v)", configured, err)
			}
		})
	}
}

func TestParseLDAPConfigFromEnvDefaults(t *testing.T) {
	setLDAPEnvironment(t, "true", "ldaps://ldap.example.com:636", "dc=example,dc=com", "", "")
	enabled, config, err := parseLDAPConfigFromEnv()
	if err != nil || !enabled {
		t.Fatalf("parseLDAPConfigFromEnv() = (%v, %#v, %v)", enabled, config, err)
	}
	if config.Timeout != 3*time.Second || config.MaxResults != 200 || config.URL != "ldaps://ldap.example.com:636" || config.BaseDN != "dc=example,dc=com" {
		t.Errorf("config = %#v", config)
	}
}

func TestParseLDAPConfigFromEnvCustomValues(t *testing.T) {
	setLDAPEnvironment(t, "true", "ldaps://ldap.example.com:636", "dc=example,dc=com", "750ms", "50")
	t.Setenv("OPENHPC_LDAP_USER_BASE_DN", "ou=People,dc=example,dc=com")
	t.Setenv("OPENHPC_LDAP_GROUP_BASE_DN", "ou=Group,dc=example,dc=com")
	t.Setenv("OPENHPC_LDAP_BIND_DN", "cn=reader,dc=example,dc=com")
	t.Setenv("OPENHPC_LDAP_BIND_PASSWORD", " secret-value ")
	_, config, err := parseLDAPConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.UserBaseDN != "ou=People,dc=example,dc=com" || config.GroupBaseDN != "ou=Group,dc=example,dc=com" || config.BindDN == "" || config.BindPassword != " secret-value " {
		t.Errorf("config = %#v", config)
	}
}

func TestParseLDAPConfigFromEnvRejectsInvalidValuesWithoutLeakingPassword(t *testing.T) {
	for _, test := range []struct{ timeout, limit string }{{"invalid", "10"}, {"3s", "invalid"}} {
		setLDAPEnvironment(t, "true", "ldaps://ldap.example.com:636", "dc=example,dc=com", test.timeout, test.limit)
		t.Setenv("OPENHPC_LDAP_BIND_PASSWORD", "must-not-leak")
		_, _, err := parseLDAPConfigFromEnv()
		if err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Errorf("error = %v", err)
		}
	}
}

func setLDAPEnvironment(t *testing.T, enabled, endpoint, baseDN, timeout, limit string) {
	t.Helper()
	t.Setenv("OPENHPC_LDAP_ENABLED", enabled)
	t.Setenv("OPENHPC_LDAP_URL", endpoint)
	t.Setenv("OPENHPC_LDAP_BASE_DN", baseDN)
	t.Setenv("OPENHPC_LDAP_USER_BASE_DN", "")
	t.Setenv("OPENHPC_LDAP_GROUP_BASE_DN", "")
	t.Setenv("OPENHPC_LDAP_BIND_DN", "")
	t.Setenv("OPENHPC_LDAP_BIND_PASSWORD", "")
	t.Setenv("OPENHPC_LDAP_CA_FILE", "")
	t.Setenv("OPENHPC_LDAP_TIMEOUT", timeout)
	t.Setenv("OPENHPC_LDAP_MAX_RESULTS", limit)
}
