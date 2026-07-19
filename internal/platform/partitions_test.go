package platform

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPartitionStorePersistsAndPatchesNodeMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}

	change, err := store.Upsert(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node03", "node01"}})
	if err != nil {
		t.Fatalf("Upsert(create) error = %v", err)
	}
	if !change.Created || change.Updated {
		t.Fatalf("create change = %#v", change)
	}

	change, err = store.Upsert(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node01", "node02"}})
	if err != nil {
		t.Fatalf("Upsert(patch) error = %v", err)
	}
	if change.Created || !change.Updated {
		t.Fatalf("patch change = %#v", change)
	}
	if !reflect.DeepEqual(change.Patch.AddedNodes, []string{"node02"}) || !reflect.DeepEqual(change.Patch.RemovedNodes, []string{"node03"}) {
		t.Fatalf("patch = %#v", change.Patch)
	}

	spec, found, err := store.Get(context.Background(), "GPU")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !found {
		t.Fatal("Get() found = false")
	}
	if !reflect.DeepEqual(spec.Nodes, []string{"node01", "node02"}) {
		t.Fatalf("stored nodes = %#v", spec.Nodes)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer store.Close()

	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 || list[0].Name != "GPU" || !reflect.DeepEqual(list[0].Nodes, []string{"node01", "node02"}) {
		t.Fatalf("List() = %#v", list)
	}
}

func TestPartitionStoreRejectsInvalidInput(t *testing.T) {
	store, err := OpenPartitionStore(":memory:")
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()

	for _, spec := range []PartitionSpec{
		{},
		{Name: "GPU", Nodes: []string{}},
		{Name: "GPU", Nodes: []string{"node01", "node01"}},
		{Name: "GPU", Nodes: []string{"node 01"}},
	} {
		if _, err := store.Upsert(context.Background(), spec); err == nil {
			t.Fatalf("Upsert(%#v) error = nil", spec)
		}
	}
}

