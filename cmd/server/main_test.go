package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

	if err := prepareStateDirectory(databasePath); err != nil {
		t.Fatalf("prepareStateDirectory() error = %v", err)
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

func TestPrepareStateDirectoryRejectsGroupReadableParent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	err := prepareStateDirectory(filepath.Join(directory, "openhpc.db"))
	if err == nil {
		t.Fatal("prepareStateDirectory() error = nil, want insecure permissions error")
	}
}

func TestPrepareStateDirectoryAllowsInMemoryDatabase(t *testing.T) {
	if err := prepareStateDirectory(":memory:"); err != nil {
		t.Fatalf("prepareStateDirectory(:memory:) error = %v", err)
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

func setSlurmEnvironment(t *testing.T, enabled, binaryDir, timeout, maxOutput string) {
	t.Helper()
	t.Setenv("OPENHPC_SLURM_ENABLED", enabled)
	t.Setenv("OPENHPC_SLURM_BIN_DIR", binaryDir)
	t.Setenv("OPENHPC_SLURM_TIMEOUT", timeout)
	t.Setenv("OPENHPC_SLURM_MAX_OUTPUT", maxOutput)
}
