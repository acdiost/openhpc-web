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

func TestPartitionStoreImportsAndSeparatesManagedPartitions(t *testing.T) {
	store, err := OpenPartitionStore(":memory:")
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()

	if _, err := store.ImportSystem(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node31", "node32"}}); err != nil {
		t.Fatalf("ImportSystem() error = %v", err)
	}
	if _, err := store.Upsert(context.Background(), PartitionSpec{Name: "test", Nodes: []string{"node33"}}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	system, err := store.ListSystem(context.Background())
	if err != nil {
		t.Fatalf("ListSystem() error = %v", err)
	}
	managed, err := store.ListManaged(context.Background())
	if err != nil {
		t.Fatalf("ListManaged() error = %v", err)
	}
	if len(system) != 1 || system[0].Managed || len(managed) != 1 || !managed[0].Managed {
		t.Fatalf("system=%#v managed=%#v", system, managed)
	}
	if system[0].Name != "GPU" || managed[0].Name != "test" {
		t.Fatalf("system=%#v managed=%#v", system, managed)
	}
}

func TestPartitionStoreDeletesManagedPartitionsOnly(t *testing.T) {
	store, err := OpenPartitionStore(":memory:")
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()

	if _, err := store.ImportSystem(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node31"}}); err != nil {
		t.Fatalf("ImportSystem() error = %v", err)
	}
	if _, err := store.Upsert(context.Background(), PartitionSpec{Name: "test", Nodes: []string{"node32"}}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := store.DeleteManaged(context.Background(), "test"); err != nil {
		t.Fatalf("DeleteManaged() error = %v", err)
	}
	if _, found, err := store.Get(context.Background(), "test"); err != nil || found {
		t.Fatalf("managed partition still found: %v %v", found, err)
	}
	if err := store.DeleteManaged(context.Background(), "GPU"); err == nil {
		t.Fatal("DeleteManaged(system) error = nil")
	}
}

func TestPartitionStoreImportsSystemSnapshotsWithoutConvertingToManaged(t *testing.T) {
	store, err := OpenPartitionStore(":memory:")
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()

	if _, err := store.ImportSystem(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node31"}}); err != nil {
		t.Fatalf("ImportSystem(create) error = %v", err)
	}
	if _, err := store.ImportSystem(context.Background(), PartitionSpec{Name: "GPU", Nodes: []string{"node31", "node32"}}); err != nil {
		t.Fatalf("ImportSystem(update) error = %v", err)
	}
	spec, found, err := store.Get(context.Background(), "GPU")
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", spec, found, err)
	}
	if spec.Managed || !reflect.DeepEqual(spec.Nodes, []string{"node31", "node32"}) {
		t.Fatalf("system partition = %#v", spec)
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
