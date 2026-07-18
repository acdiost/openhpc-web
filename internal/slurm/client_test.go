package slurm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

const validSinfoJSON = `{"errors":[],"sinfo":[
	{"nodes":{"nodes":["node01"]},"partition":{"name":"GPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":0,"total":64},"memory":{"maximum":1000},"gres":{"total":""}},
	{"nodes":{"nodes":["node02"]},"partition":{"name":"GPU"},"node":{"state":["MIXED"]},"cpus":{"allocated":32,"total":64},"memory":{"maximum":1000},"gres":{"total":""}},
	{"nodes":{"nodes":["node03"]},"partition":{"name":"GPU"},"node":{"state":["ALLOCATED"]},"cpus":{"allocated":64,"total":64},"memory":{"maximum":1000},"gres":{"total":""}},
	{"nodes":{"nodes":["node04"]},"partition":{"name":"GPU"},"node":{"state":["IDLE","NO_RESPOND"]},"cpus":{"allocated":0,"total":64},"memory":{"maximum":1000},"gres":{"total":""}}
]}`

const validSqueueJSON = `{"errors":[],"jobs":[
	{"job_id":1,"name":"a","user_name":"u","job_state":["RUNNING"],"node_count":{"set":true,"number":1}},
	{"job_id":2,"name":"b","user_name":"u","job_state":["PENDING"],"node_count":{"set":true,"number":1},"state_reason":"Resources"},
	{"job_id":3,"name":"c","user_name":"u","job_state":["COMPLETING"],"node_count":{"set":true,"number":1}},
	{"job_id":4,"name":"d","user_name":"u","job_state":["RUNNING"],"node_count":{"set":true,"number":1}},
	{"job_id":5,"name":"e","user_name":"u","job_state":["PENDING"],"node_count":{"set":true,"number":1},"state_reason":"Priority"}
]}`

func TestClientSnapshotAggregatesJSONNodesAndJobs(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(validSinfoJSON), "squeue": []byte(validSqueueJSON)}}
	metrics, err := newTestClient(t, runner).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if metrics != (cluster.Metrics{OnlineNodes: 3, RunningJobs: 2, QueuedJobs: 2, CPUUsage: 50}) {
		t.Errorf("metrics = %+v", metrics)
	}
}

func TestClientSnapshotUsesOnlyJSONCommands(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(`{"errors":[],"sinfo":[]}`), "squeue": []byte(`{"errors":[],"jobs":[]}`)}}
	if _, err := newTestClient(t, runner).Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	want := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "sinfo"), args: []string{"--Node", "--json"}},
		{path: filepath.Join("/opt/slurm/bin", "squeue"), args: []string{"--json"}},
	}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("runner calls = %#v, want %#v", got, want)
	}
}

func TestClientSnapshotTimesOutCommands(t *testing.T) {
	runner := &scriptedRunner{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: 25 * time.Millisecond, MaxOutputBytes: 1024, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	started := time.Now()
	_, err = client.Snapshot(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("timeout took %v", elapsed)
	}
}

func TestClientSnapshotWrapsCommandErrorsAndStops(t *testing.T) {
	sentinel := errors.New("scheduler unavailable")
	for _, test := range []struct {
		fail      string
		wantCalls int
	}{{"sinfo", 1}, {"squeue", 2}} {
		t.Run(test.fail, func(t *testing.T) {
			runner := &scriptedRunner{run: func(_ context.Context, path string, _ ...string) ([]byte, error) {
				command := filepath.Base(path)
				if command == test.fail {
					return nil, sentinel
				}
				if command == "sinfo" {
					return []byte(validSinfoJSON), nil
				}
				return []byte(validSqueueJSON), nil
			}}
			_, err := newTestClient(t, runner).Snapshot(context.Background())
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), test.fail) {
				t.Fatalf("Snapshot() error = %v", err)
			}
			if calls := len(runner.callsSnapshot()); calls != test.wantCalls {
				t.Errorf("calls = %d", calls)
			}
		})
	}
}

func TestClientSnapshotCoalescesConcurrentRefreshes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	runner := &scriptedRunner{run: func(_ context.Context, path string, _ ...string) ([]byte, error) {
		if filepath.Base(path) == "sinfo" {
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
			return []byte(validSinfoJSON), nil
		}
		return []byte(validSqueueJSON), nil
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
		t.Errorf("runner calls = %d", calls)
	}
}

func TestClientCanceledLeaderDoesNotPoisonSharedCache(t *testing.T) {
	started := make(chan struct{})
	var sinfoCalls atomic.Int32
	runner := &scriptedRunner{run: func(ctx context.Context, path string, _ ...string) ([]byte, error) {
		if filepath.Base(path) != "sinfo" {
			return []byte(validSqueueJSON), nil
		}
		if sinfoCalls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(validSinfoJSON), nil
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
		t.Fatalf("leader error = %v", err)
	}
	waiter := <-waiterDone
	if waiter.err != nil || waiter.metrics.OnlineNodes != 3 {
		t.Fatalf("waiter = (%+v, %v)", waiter.metrics, waiter.err)
	}
}

func TestClientSnapshotRejectsOutputOverConfiguredLimit(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(strings.Repeat("x", 33))}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 32, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil")
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
