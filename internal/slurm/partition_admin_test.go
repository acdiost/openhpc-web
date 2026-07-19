package slurm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClientApplyPartitionCreatesWhenMissing(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, path string, args ...string) ([]byte, error) {
		switch filepath.Base(path) {
		case "scontrol":
			switch args[0] {
			case "show":
				return nil, errors.New("partition not found")
			case "create":
				if !reflect.DeepEqual(args, []string{"create", "PartitionName=GPU", "Nodes=node01,node02"}) {
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
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"show", "partition", "GPU"}},
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"create", "PartitionName=GPU", "Nodes=node01,node02"}},
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
			case "show":
				return []byte("PartitionName=GPU"), nil
			case "update":
				if !reflect.DeepEqual(args, []string{"update", "PartitionName=GPU", "Nodes=node01,node03"}) {
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
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"show", "partition", "GPU"}},
		{path: filepath.Join("/opt/slurm/bin", "scontrol"), args: []string{"update", "PartitionName=GPU", "Nodes=node01,node03"}},
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

