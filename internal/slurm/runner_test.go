package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandRunnerPassesArgumentsWithoutShellAndUsesCLocale(t *testing.T) {
	t.Setenv("GO_WANT_SLURM_HELPER_PROCESS", "1")
	t.Setenv("LC_ALL", "unexpected-locale")
	t.Setenv("LANG", "unexpected-language")
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	wantArgs := []string{"space value", "--format=%N|%T|%C", "; touch " + marker, "$(touch " + marker + ")"}
	args := append([]string{"-test.run=^TestCommandRunnerHelperProcess$", "--", "echo"}, wantArgs...)

	output, err := (&CommandRunner{MaxOutputBytes: 4096, Environment: []string{"GO_WANT_SLURM_HELPER_PROCESS=1"}}).Run(context.Background(), os.Args[0], args...)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var result helperResult
	if err := json.NewDecoder(strings.NewReader(string(output))).Decode(&result); err != nil {
		t.Fatalf("decode helper output %q: %v", output, err)
	}
	if !reflect.DeepEqual(result.Args, wantArgs) {
		t.Errorf("helper args = %#v, want %#v", result.Args, wantArgs)
	}
	if result.LCAll != "C" || result.Lang != "C" {
		t.Errorf("helper locale = LC_ALL=%q LANG=%q, want C/C", result.LCAll, result.Lang)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("shell-like argument was executed; marker Stat error = %v", err)
	}
}

func TestCommandRunnerDoesNotInheritApplicationSecrets(t *testing.T) {
	t.Setenv("OPENHPC_ADMIN_PASSWORD", "must-not-reach-slurm")
	output, err := (&CommandRunner{MaxOutputBytes: 4096, Environment: []string{"GO_WANT_SLURM_HELPER_PROCESS=1"}}).Run(context.Background(), os.Args[0], "-test.run=^TestCommandRunnerHelperProcess$", "--", "environment")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(string(output), "OPENHPC_ADMIN_PASSWORD") || strings.Contains(string(output), "must-not-reach-slurm") {
		t.Fatalf("secret leaked to Slurm child environment: %q", output)
	}
}

func TestCommandRunnerReturnsNonZeroExitError(t *testing.T) {
	t.Setenv("GO_WANT_SLURM_HELPER_PROCESS", "1")
	runner := &CommandRunner{MaxOutputBytes: 1024, Environment: []string{"GO_WANT_SLURM_HELPER_PROCESS=1"}}

	_, err := runner.Run(context.Background(), os.Args[0], "-test.run=^TestCommandRunnerHelperProcess$", "--", "exit")
	if err == nil {
		t.Fatal("Run() error = nil, want non-zero exit error")
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("Run() error = %T %v, want wrapped exec.ExitError", err, err)
	}
	if exitError.ExitCode() != 7 {
		t.Errorf("exit code = %d, want 7", exitError.ExitCode())
	}
}

func TestCommandRunnerReturnsContextTimeout(t *testing.T) {
	t.Setenv("GO_WANT_SLURM_HELPER_PROCESS", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := (&CommandRunner{MaxOutputBytes: 1024, Environment: []string{"GO_WANT_SLURM_HELPER_PROCESS=1"}}).Run(ctx, os.Args[0], "-test.run=^TestCommandRunnerHelperProcess$", "--", "sleep")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context deadline exceeded", err)
	}
}

func TestCommandRunnerReturnsOutputLimitError(t *testing.T) {
	t.Setenv("GO_WANT_SLURM_HELPER_PROCESS", "1")

	output, err := (&CommandRunner{MaxOutputBytes: 32, Environment: []string{"GO_WANT_SLURM_HELPER_PROCESS=1"}}).Run(context.Background(), os.Args[0], "-test.run=^TestCommandRunnerHelperProcess$", "--", "output")
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("Run() error = %v, want ErrOutputLimit", err)
	}
	if output != nil {
		t.Errorf("Run() output = %q, want nil after output limit", output)
	}
}

func TestCommandRunnerRejectsNonPositiveOutputLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := (&CommandRunner{MaxOutputBytes: limit}).Run(context.Background(), os.Args[0]); err == nil {
			t.Errorf("Run() with MaxOutputBytes=%d error = nil", limit)
		}
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	runner := &scriptedRunner{}
	tests := []struct {
		name   string
		config Config
	}{
		{name: "relative binary directory", config: Config{BinaryDir: "bin", Timeout: time.Second, MaxOutputBytes: 1024, Runner: runner}},
		{name: "unclean binary directory", config: Config{BinaryDir: "/opt/slurm/../slurm", Timeout: time.Second, MaxOutputBytes: 1024, Runner: runner}},
		{name: "zero timeout", config: Config{BinaryDir: "/opt/slurm/bin", MaxOutputBytes: 1024, Runner: runner}},
		{name: "negative timeout", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: -time.Second, MaxOutputBytes: 1024, Runner: runner}},
		{name: "zero output limit", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, Runner: runner}},
		{name: "negative output limit", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: -1, Runner: runner}},
		{name: "timeout over hard limit", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: 31 * time.Second, MaxOutputBytes: 1024, Runner: runner}},
		{name: "output over hard limit", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: (8 << 20) + 1, Runner: runner}},
		{name: "cache over hard limit", config: Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 1024, CacheTTL: 61 * time.Second, Runner: runner}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.config)
			if err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
			if client != nil {
				t.Error("New() returned client with validation error")
			}
		})
	}
}

func TestNewDefaultRunnerValidatesCommands(t *testing.T) {
	tests := []struct {
		name        string
		fileModes   map[string]os.FileMode
		wantError   bool
		wantCommand string
	}{
		{name: "missing sinfo", fileModes: map[string]os.FileMode{}, wantError: true, wantCommand: "sinfo"},
		{name: "missing squeue", fileModes: map[string]os.FileMode{"sinfo": 0o700}, wantError: true},
		{name: "non-executable sinfo", fileModes: map[string]os.FileMode{"sinfo": 0o600, "squeue": 0o700}, wantError: true, wantCommand: "sinfo"},
		{name: "non-executable squeue", fileModes: map[string]os.FileMode{"sinfo": 0o700, "squeue": 0o600}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for name, mode := range test.fileModes {
				if err := os.WriteFile(filepath.Join(directory, name), []byte("ordinary test file"), mode); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}

			client, err := New(Config{BinaryDir: directory, Timeout: time.Second, MaxOutputBytes: 1024})
			if test.wantError {
				if err == nil {
					t.Fatal("New() error = nil, want command validation error")
				}
				if client != nil {
					t.Error("New() returned client with command validation error")
				}
				if test.wantCommand != "" && !strings.Contains(err.Error(), test.wantCommand) {
					t.Errorf("New() error = %q, want command %q", err, test.wantCommand)
				}
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, ok := client.runner.(*CommandRunner); !ok {
				t.Errorf("default runner = %T, want *CommandRunner", client.runner)
			}
		})
	}
}

func TestNewDefaultRunnerAllowsOwnerAndWritableRisksWithWarnings(t *testing.T) {
	directory := t.TempDir()
	for _, command := range []string{"sinfo", "squeue", "sacct", "sacctmgr", "sstat"} {
		path := filepath.Join(directory, command)
		if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o722); err != nil {
			t.Fatal(err)
		}
	}
	warnings := make([]string, 0)
	client, err := New(Config{
		BinaryDir: directory, Timeout: time.Second, MaxOutputBytes: 1024,
		Warning: func(message string) { warnings = append(warnings, message) },
	})
	if err != nil {
		t.Fatalf("New() error = %v, want warning-only initialization", err)
	}
	if client == nil || len(warnings) == 0 {
		t.Fatalf("client = %v, warnings = %q", client, warnings)
	}
}

func TestValidateSlurmExecutableStillRejectsSymlinks(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "sinfo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := validateSlurmExecutable(link); err == nil {
		t.Fatal("validateSlurmExecutable() error = nil, want symlink rejection")
	}
}

func TestValidateSlurmExecutableWarnsForNonRootOwnedWritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sinfo")
	if err := os.WriteFile(path, []byte("test"), 0o722); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o722); err != nil {
		t.Fatal(err)
	}
	warnings, err := validateSlurmExecutable(path)
	if err != nil {
		t.Fatalf("validateSlurmExecutable() error = %v, want warnings only", err)
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "writable") {
		t.Fatalf("validateSlurmExecutable() warnings = %q", warnings)
	}
}

func TestSlurmPathRiskWarningsReportsOwnerWithoutDependingOnTestUID(t *testing.T) {
	warnings := slurmPathRiskWarnings("/opt/slurm/bin/sinfo", 1000, 0o722)
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "owner") || !strings.Contains(joined, "writable") {
		t.Fatalf("slurmPathRiskWarnings() = %q", warnings)
	}
}

type helperResult struct {
	Args  []string `json:"args"`
	LCAll string   `json:"lc_all"`
	Lang  string   `json:"lang"`
}

func TestCommandRunnerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SLURM_HELPER_PROCESS") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	mode := os.Args[separator+1]
	arguments := os.Args[separator+2:]
	switch mode {
	case "echo":
		_ = json.NewEncoder(os.Stdout).Encode(helperResult{Args: arguments, LCAll: os.Getenv("LC_ALL"), Lang: os.Getenv("LANG")})
	case "exit":
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "output":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", 128)))
	case "environment":
		_, _ = os.Stdout.Write([]byte(strings.Join(os.Environ(), "\n")))
	default:
		os.Exit(3)
	}
	os.Exit(0)
}
