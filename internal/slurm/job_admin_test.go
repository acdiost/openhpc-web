package slurm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClientCancelJobUsesValidatedFixedArguments(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		if filepath.Base(path) != "scancel" {
			return nil, errors.New("unexpected command")
		}
		return nil, nil
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	if err := client.CancelJob(context.Background(), 32943); err != nil {
		t.Fatalf("CancelJob() error = %v", err)
	}
	want := []commandCall{{path: filepath.Join("/opt/slurm/bin", "scancel"), args: []string{"32943"}}}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestClientCancelJobRejectsInvalidID(t *testing.T) {
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: &scriptedRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CancelJob(context.Background(), 0); err == nil {
		t.Fatal("CancelJob(0) error = nil")
	}
}

func TestClientCancelJobInvalidatesJobCacheAfterSuccess(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"squeue": []byte(`{"errors":[],"jobs":[{"job_id":32943,"name":"job","user_name":"alice","user_id":{"set":true,"number":1000},"job_state":["RUNNING"],"cpus":{"set":true,"number":1},"time_limit":{"set":true,"number":1},"node_count":{"set":true,"number":1},"nodes":"node01"}]}`),
	}}
	client := newTestClient(t, runner)
	if _, err := client.Jobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.CancelJob(context.Background(), 32943); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Jobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range runner.callsSnapshot() {
		if filepath.Base(call.path) == "squeue" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("squeue calls = %d, want 2 after cancellation", count)
	}
}
