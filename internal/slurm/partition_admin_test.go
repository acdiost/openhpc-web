package slurm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func TestClientApplyPartitionCreatesWhenMissing(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch filepath.Base(path) {
		case "scontrol":
			switch args[0] {
			case "create":
				if !reflect.DeepEqual(args, []string{"create", "partitionname=GPU", "nodes=node01,node02"}) {
					t.Fatalf("create args = %#v", args)
				}
				return nil, nil
			}
		}
		return nil, errors.New("unexpected command")
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.ApplyPartition(context.Background(), "GPU", []string{"node01", "node02"}); err != nil {
		t.Fatalf("ApplyPartition() error = %v", err)
	}
	want := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"create", "partitionname=GPU", "nodes=node01,node02"}},
	}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestClientApplyPartitionUpdatesWhenPresent(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch filepath.Base(path) {
		case "scontrol":
			switch args[0] {
			case "create":
				return nil, errors.New("already exists")
			case "update":
				if !reflect.DeepEqual(args, []string{"update", "partitionname=GPU", "nodes=node01,node03"}) {
					t.Fatalf("update args = %#v", args)
				}
				return nil, nil
			}
		}
		return nil, errors.New("unexpected command")
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.ApplyPartition(context.Background(), "GPU", []string{"node01", "node03"}); err != nil {
		t.Fatalf("ApplyPartition() error = %v", err)
	}
	want := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"create", "partitionname=GPU", "nodes=node01,node03"}},
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"update", "partitionname=GPU", "nodes=node01,node03"}},
	}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestClientApplyPartitionRejectsInvalidDefinition(t *testing.T) {
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: &scriptedRunner{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, test := range []struct {
		name  string
		nodes []string
	}{
		{name: "", nodes: []string{"node01"}},
		{name: "GPU", nodes: nil},
		{name: "GPU", nodes: []string{"node01", "node01"}},
		{name: "GPU", nodes: []string{"node 01"}},
	} {
		if err := client.ApplyPartition(context.Background(), test.name, test.nodes); err == nil {
			t.Fatalf("ApplyPartition(%q, %#v) error = nil", test.name, test.nodes)
		}
	}
}

func TestClientDeletePartition(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch filepath.Base(path) {
		case "scontrol":
			if !reflect.DeepEqual(args, []string{"delete", "partitionname=test"}) {
				t.Fatalf("delete args = %#v", args)
			}
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.DeletePartition(context.Background(), "test"); err != nil {
		t.Fatalf("DeletePartition() error = %v", err)
	}
}

func TestClientSetNodeState(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		if filepath.Base(path) != "scontrol" {
			return nil, errors.New("unexpected command")
		}
		return nil, nil
	}}
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: runner})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.SetNodeState(context.Background(), "node01", cluster.NodeStateResume, ""); err != nil {
		t.Fatalf("SetNodeState(resume) error = %v", err)
	}
	if err := client.SetNodeState(context.Background(), "node02", cluster.NodeStateDown, "scheduled maintenance"); err != nil {
		t.Fatalf("SetNodeState(down) error = %v", err)
	}
	if err := client.SetNodeState(context.Background(), "node03", cluster.NodeStateDrain, "hardware inspection"); err != nil {
		t.Fatalf("SetNodeState(drain) error = %v", err)
	}
	want := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"update", "nodename=node01", "state=resume"}},
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"update", "nodename=node02", "state=down", "reason=scheduled maintenance"}},
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"update", "nodename=node03", "state=drain", "reason=hardware inspection"}},
	}
	if got := runner.callsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestClientSetNodeStateValidatesActionAndReason(t *testing.T) {
	client, err := New(Config{BinaryDir: "/opt/slurm/bin", Timeout: time.Second, MaxOutputBytes: 16 << 10, Runner: &scriptedRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		state  cluster.NodeState
		reason string
	}{
		{state: cluster.NodeStateDown},
		{state: cluster.NodeStateDrain, reason: "\n"},
		{state: cluster.NodeStateResume, reason: "unexpected reason"},
		{state: "invalid", reason: "maintenance"},
	} {
		if err := client.SetNodeState(context.Background(), "node01", test.state, test.reason); err == nil {
			t.Errorf("SetNodeState(%q, %q) error = nil", test.state, test.reason)
		}
	}
}

func TestClientSetNodeStateInvalidatesNodeCache(t *testing.T) {
	responses := [][]byte{
		[]byte(`{"errors":[],"sinfo":[{"nodes":{"nodes":["node01"]},"partition":{"name":"CPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":0,"total":1},"memory":{"maximum":1}}]}`),
		[]byte(`{"errors":[],"sinfo":[{"nodes":{"nodes":["node01"]},"partition":{"name":"CPU"},"node":{"state":["DOWN"]},"cpus":{"allocated":0,"total":1},"memory":{"maximum":1}}]}`),
	}
	sinfoCalls := 0
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		if filepath.Base(path) == "scontrol" {
			return nil, nil
		}
		if filepath.Base(path) != "sinfo" {
			return nil, errors.New("unexpected command")
		}
		output := responses[sinfoCalls]
		sinfoCalls++
		return output, nil
	}}
	client := newTestClient(t, runner)
	if _, err := client.Nodes(context.Background()); err != nil {
		t.Fatalf("Nodes() error = %v", err)
	}
	if err := client.SetNodeState(context.Background(), "node01", cluster.NodeStateDown, "maintenance"); err != nil {
		t.Fatalf("SetNodeState() error = %v", err)
	}
	nodes, err := client.Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes() after update error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].Online {
		t.Fatalf("nodes after update = %#v, want offline node01", nodes)
	}
	if sinfoCalls != 2 {
		t.Fatalf("sinfo calls = %d, want 2", sinfoCalls)
	}
}
