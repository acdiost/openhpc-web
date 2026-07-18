package slurm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestClientPartitionsAggregatesCachedNodeSnapshot(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(`{
		"errors": [],
		"sinfo": [
			{"nodes":{"nodes":["node31"]},"partition":{"name":"GPU"},"node":{"state":["MIXED"]},"cpus":{"allocated":48,"total":128},"memory":{"maximum":510000},"gres":{"total":"gpu:8"}},
			{"nodes":{"nodes":["node32"]},"partition":{"name":"GPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":0,"total":128},"memory":{"maximum":510000},"gres":{"total":"gpu:8"}},
			{"nodes":{"nodes":["node33"]},"partition":{"name":"CPU"},"node":{"state":["DOWN"]},"cpus":{"allocated":0,"total":64},"memory":{"maximum":256000},"gres":{"total":""}}
		]
	}`)}}
	client := newTestClient(t, runner)

	partitions, err := client.Partitions(context.Background())
	if err != nil {
		t.Fatalf("Partitions() error = %v", err)
	}
	want := []cluster.Partition{
		{Name: "CPU", NodeCount: 1, TotalCPUs: 64, MemoryMB: 256000},
		{Name: "GPU", NodeCount: 2, OnlineNodes: 2, AllocatedCPUs: 48, TotalCPUs: 256, MemoryMB: 1020000, CPUUtilization: 19},
	}
	if !reflect.DeepEqual(partitions, want) {
		t.Errorf("Partitions() = %#v, want %#v", partitions, want)
	}
	if _, err := client.Nodes(context.Background()); err != nil {
		t.Fatalf("Nodes() error = %v", err)
	}
	if calls := len(runner.callsSnapshot()); calls != 1 {
		t.Errorf("runner calls = %d, want one cached sinfo call", calls)
	}
}

func TestAggregatePartitionsDeduplicatesIdenticalNodesAndRejectsConflicts(t *testing.T) {
	node := cluster.Node{Name: "node31", Partition: "GPU", State: "mixed", AllocatedCPUs: 32, TotalCPUs: 128, MemoryMB: 510000, Online: true}
	partitions, err := aggregatePartitions([]cluster.Node{node, node})
	if err != nil {
		t.Fatalf("aggregatePartitions() error = %v", err)
	}
	want := []cluster.Partition{{Name: "GPU", NodeCount: 1, OnlineNodes: 1, AllocatedCPUs: 32, TotalCPUs: 128, MemoryMB: 510000, CPUUtilization: 25}}
	if !reflect.DeepEqual(partitions, want) {
		t.Errorf("partitions = %#v, want %#v", partitions, want)
	}

	conflict := node
	conflict.AllocatedCPUs = 64
	if _, err := aggregatePartitions([]cluster.Node{node, conflict}); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("aggregatePartitions(conflict) error = %v", err)
	}
}

func TestAggregatePartitionsReturnsStableEmptyAndSortedResults(t *testing.T) {
	partitions, err := aggregatePartitions(nil)
	if err != nil || len(partitions) != 0 {
		t.Fatalf("aggregatePartitions(nil) = (%#v, %v)", partitions, err)
	}
	nodes := []cluster.Node{
		{Name: "n2", Partition: "zeta", TotalCPUs: 3, Online: true},
		{Name: "n1", Partition: "alpha", TotalCPUs: 2, Online: true},
	}
	partitions, err = aggregatePartitions(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{partitions[0].Name, partitions[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Errorf("partition order = %v", got)
	}
}
