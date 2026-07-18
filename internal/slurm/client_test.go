package slurm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestClientSnapshotParsesSinfoAndSqueue(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"sinfo": []byte(strings.Join([]string{
			"node01|idle|0/64/0/64",
			"node02|mixed|32/32/0/64",
			"node02|mixed|32/32/0/64",
			"node03|allocated|64/0/0/64",
			"node04|down*|0/0/64/64",
			"",
		}, "\n")),
		"squeue": []byte("RUNNING\nPENDING\nCOMPLETING\nRUNNING\nPENDING\nCOMPLETED\n"),
	}}
	client := newTestClient(t, runner)

	metrics, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if metrics.OnlineNodes != 3 {
		t.Errorf("OnlineNodes = %d, want 3", metrics.OnlineNodes)
	}
	if metrics.RunningJobs != 2 {
		t.Errorf("RunningJobs = %d, want 2", metrics.RunningJobs)
	}
	if metrics.QueuedJobs != 2 {
		t.Errorf("QueuedJobs = %d, want 2", metrics.QueuedJobs)
	}
	if metrics.CPUUsage != 50 {
		t.Errorf("CPUUsage = %d, want 50", metrics.CPUUsage)
	}
}

func TestClientSnapshotUsesOnlyWhitelistedCommandsAndArguments(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"sinfo":  []byte("node01|idle|0/64/0/64\n"),
		"squeue": nil,
	}}
	client := newTestClient(t, runner)
	if _, err := client.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	want := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "sinfo"), args: []string{"--noheader", "--Node", "--format=%N|%T|%C"}},
		{path: filepath.Join("/opt/slurm/bin", "squeue"), args: []string{"--noheader", "--format=%T"}},
	}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("runner calls = %#v, want %#v", got, want)
	}
}

func TestClientSnapshotTimesOutCommands(t *testing.T) {
	runner := &scriptedRunner{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: 25 * time.Millisecond, MaxOutputBytes: 1024, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	started := time.Now()
	_, err = client.Snapshot(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Snapshot() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Snapshot() timeout took %v, want under 1s", elapsed)
	}
}

func TestClientSnapshotWrapsCommandErrorsAndStops(t *testing.T) {
	sentinel := errors.New("scheduler unavailable")
	tests := []struct {
		name      string
		fail      string
		wantCalls int
	}{
		{name: "sinfo", fail: "sinfo", wantCalls: 1},
		{name: "squeue", fail: "squeue", wantCalls: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{run: func(_ context.Context, path string, _ ...string) ([]byte, error) {
				command := filepath.Base(path)
				if command == test.fail {
					return nil, sentinel
				}
				return []byte("node01|idle|0/64/0/64\n"), nil
			}}
			client := newTestClient(t, runner)

			_, err := client.Snapshot(context.Background())
			if !errors.Is(err, sentinel) {
				t.Fatalf("Snapshot() error = %v, want wrapped sentinel", err)
			}
			if !strings.Contains(err.Error(), test.fail) {
				t.Errorf("Snapshot() error = %q, want %q context", err, test.fail)
			}
			if calls := len(runner.callsSnapshot()); calls != test.wantCalls {
				t.Errorf("runner calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestClientSnapshotRejectsMalformedSinfo(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "missing columns", output: "node01|idle\n"},
		{name: "invalid CPU count", output: "node01|idle|not/a/cpu/value\n"},
		{name: "allocated exceeds total", output: "node01|allocated|65/0/0/64\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(test.output)}}
			client := newTestClient(t, runner)
			if _, err := client.Snapshot(context.Background()); err == nil {
				t.Fatal("Snapshot() error = nil, want parse error")
			}
		})
	}
}

func TestClientSnapshotExcludesUnavailableNodeStateSuffixes(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"sinfo": []byte("ready|idle|0/64/0/64\nnoresponse|idle*|0/64/0/64\npoweredoff|idle~|0/64/0/64\npoweringup|idle#|0/64/0/64\npoweringdown|idle%|0/64/0/64\ncloudpending|idle!|0/64/0/64\n"),
	}}
	metrics, err := newTestClient(t, runner).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if metrics.OnlineNodes != 1 {
		t.Errorf("OnlineNodes = %d, want 1", metrics.OnlineNodes)
	}
}

func TestClientSnapshotRejectsOversizedSqueueLine(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"sinfo":  []byte("node01|idle|0/64/0/64\n"),
		"squeue": []byte(strings.Repeat("R", 70<<10)),
	}}
	if _, err := newTestClient(t, runner).Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil, want scanner error")
	}
}

func TestClientSnapshotCoalescesConcurrentRefreshes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &scriptedRunner{run: func(_ context.Context, path string, _ ...string) ([]byte, error) {
		if filepath.Base(path) == "sinfo" {
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
			return []byte("node01|idle|0/64/0/64\n"), nil
		}
		return nil, nil
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, CacheTTL: 10 * time.Second, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = client.Snapshot(context.Background()) }()
	}
	<-started
	close(release)
	wg.Wait()
	if calls := len(runner.callsSnapshot()); calls != 2 {
		t.Errorf("runner calls = %d, want one sinfo+squeue refresh", calls)
	}
}

func TestClientCanceledLeaderDoesNotPoisonSharedCache(t *testing.T) {
	started := make(chan struct{})
	var sinfoCalls int
	runner := &scriptedRunner{run: func(ctx context.Context, path string, _ ...string) ([]byte, error) {
		if filepath.Base(path) != "sinfo" {
			return nil, nil
		}
		sinfoCalls++
		if sinfoCalls == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("node01|idle|0/64/0/64\n"), nil
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, CacheTTL: 10 * time.Second, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	leaderContext, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() { _, err := client.Snapshot(leaderContext); leaderDone <- err }()
	<-started
	waiterDone := make(chan struct {
		metrics cluster.Metrics
		err     error
	}, 1)
	go func() {
		metrics, err := client.Snapshot(context.Background())
		waiterDone <- struct {
			metrics cluster.Metrics
			err     error
		}{metrics, err}
	}()
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context canceled", err)
	}
	waiter := <-waiterDone
	if waiter.err != nil || waiter.metrics.OnlineNodes != 1 {
		t.Fatalf("waiter = (%+v, %v), want fresh metrics", waiter.metrics, waiter.err)
	}
}

func TestClientSnapshotRejectsOutputOverConfiguredLimit(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(strings.Repeat("x", 33))}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 32, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil, want output limit error")
	}
}

func newTestClient(t *testing.T, runner Runner) *Client {
	t.Helper()
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type commandCall struct {
	path string
	args []string
}

type scriptedRunner struct {
	mu      sync.Mutex
	outputs map[string][]byte
	run     func(context.Context, string, ...string) ([]byte, error)
	calls   []commandCall
}

func (r *scriptedRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, commandCall{path: path, args: append([]string(nil), args...)})
	r.mu.Unlock()
	if r.run != nil {
		return r.run(ctx, path, args...)
	}
	return append([]byte(nil), r.outputs[filepath.Base(path)]...), nil
}

func (r *scriptedRunner) callsSnapshot() []commandCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]commandCall, len(r.calls))
	for index, call := range r.calls {
		result[index] = commandCall{path: call.path, args: append([]string(nil), call.args...)}
	}
	return result
}
