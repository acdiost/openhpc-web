package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditStoreDatabaseFilesUseOwnerOnlyPermissions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	store, err := OpenAuditStore(databasePath)
	if err != nil {
		t.Fatalf("OpenAuditStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	err = store.Record(context.Background(), AuditEvent{
		Actor:     "cluster-admin",
		Action:    "auth.login",
		Outcome:   "success",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) && path != databasePath {
				continue
			}
			t.Errorf("Stat(%q) error = %v", path, err)
			continue
		}
		if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
			t.Errorf("%s permissions = %04o, want %04o", filepath.Base(path), got, want)
		}
	}
}

func TestOpenAuditStoreRunsMigration(t *testing.T) {
	store := openTestAuditStore(t)

	var tableSQL string
	err := store.db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'audit_events'",
	).Scan(&tableSQL)
	if err != nil {
		t.Fatalf("query audit_events migration: %v", err)
	}

	for _, column := range []string{"actor TEXT NOT NULL", "action TEXT NOT NULL", "outcome TEXT NOT NULL", "created_at TEXT NOT NULL"} {
		if !strings.Contains(tableSQL, column) {
			t.Errorf("audit_events schema does not contain %q; schema: %s", column, tableSQL)
		}
	}
}

func TestAuditStoreRecordPersistsEvent(t *testing.T) {
	store := openTestAuditStore(t)
	createdAt := time.Date(2026, time.July, 18, 14, 30, 45, 123456789, time.FixedZone("CST", 8*60*60))
	event := AuditEvent{
		Actor:     "cluster-admin",
		Action:    "auth.login",
		Outcome:   "success",
		CreatedAt: createdAt,
	}

	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	var actor, action, outcome, storedCreatedAt string
	err := store.db.QueryRow(
		"SELECT actor, action, outcome, created_at FROM audit_events",
	).Scan(&actor, &action, &outcome, &storedCreatedAt)
	if err != nil {
		t.Fatalf("query recorded event: %v", err)
	}
	if actor != event.Actor || action != event.Action || outcome != event.Outcome {
		t.Errorf("stored event = (%q, %q, %q), want (%q, %q, %q)", actor, action, outcome, event.Actor, event.Action, event.Outcome)
	}
	if want := createdAt.UTC().Format(time.RFC3339Nano); storedCreatedAt != want {
		t.Errorf("stored created_at = %q, want %q", storedCreatedAt, want)
	}
}

func TestAuditStoreRecordReturnsErrorAfterClose(t *testing.T) {
	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("OpenAuditStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	err = store.Record(context.Background(), AuditEvent{CreatedAt: time.Now()})
	if err == nil {
		t.Fatal("Record() after Close() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "record audit event") {
		t.Errorf("Record() error = %q, want operation context", err)
	}
}

func TestAuditStoreRecordHonorsCancelledContext(t *testing.T) {
	store := openTestAuditStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Record(ctx, AuditEvent{CreatedAt: time.Now()})
	if err == nil {
		t.Fatal("Record() with cancelled context error = nil, want error")
	}
	if !strings.Contains(err.Error(), "record audit event") {
		t.Errorf("Record() error = %q, want operation context", err)
	}
}

func TestOpenAuditStoreReturnsMigrationErrorForMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "audit.db")

	store, err := OpenAuditStore(path)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("OpenAuditStore() error = nil, want error")
	}
	if store != nil {
		t.Error("OpenAuditStore() returned a store with an error")
	}
	if !strings.Contains(err.Error(), "migrate audit database") {
		t.Errorf("OpenAuditStore() error = %q, want migration context", err)
	}
}

func TestOpenAuditStoreRejectsSymbolicLinkDatabaseFiles(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		name := "database"
		if suffix != "" {
			name = suffix[1:]
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			databasePath := filepath.Join(directory, "audit.db")
			targetPath := filepath.Join(directory, "target")
			if err := os.WriteFile(targetPath, []byte("not a database"), 0o600); err != nil {
				t.Fatalf("write symlink target: %v", err)
			}
			if err := os.Symlink(targetPath, databasePath+suffix); err != nil {
				t.Fatalf("create %s symlink: %v", name, err)
			}

			store, err := OpenAuditStore(databasePath)
			if err == nil {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("OpenAuditStore() error = nil, want symbolic link rejection")
			}
			if store != nil {
				t.Error("OpenAuditStore() returned store for symbolic link path")
			}
			if !strings.Contains(err.Error(), "must not be symbolic links") {
				t.Errorf("OpenAuditStore() error = %q, want symbolic link context", err)
			}
		})
	}
}

func TestDatabaseFilesIncludesSQLiteSidecars(t *testing.T) {
	path := filepath.Join("data", "audit.db")
	want := []string{path, path + "-wal", path + "-shm"}
	got := databaseFiles(path)
	if len(got) != len(want) {
		t.Fatalf("databaseFiles() length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("databaseFiles()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestAuditStoreListUsesStableNewestFirstCursor(t *testing.T) {
	store := openTestAuditStore(t)
	createdAt := time.Date(2026, time.July, 18, 15, 0, 0, 0, time.UTC)
	for _, actor := range []string{"first", "second", "third", "fourth"} {
		if err := store.Record(context.Background(), AuditEvent{
			Actor: actor, Action: "auth.login", Outcome: "success", CreatedAt: createdAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	page, err := store.List(context.Background(), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Actor != "fourth" || page.Events[1].Actor != "third" {
		t.Fatalf("first page events = %#v", page.Events)
	}
	if !page.HasMore || page.NextBeforeID != page.Events[1].ID {
		t.Fatalf("first page cursor = (%t, %d), events = %#v", page.HasMore, page.NextBeforeID, page.Events)
	}

	if err := store.Record(context.Background(), AuditEvent{
		Actor: "new arrival", Action: "auth.logout", Outcome: "success", CreatedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	secondPage, err := store.List(context.Background(), page.NextBeforeID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Events) != 2 || secondPage.Events[0].Actor != "second" || secondPage.Events[1].Actor != "first" {
		t.Fatalf("second page events = %#v", secondPage.Events)
	}
	if secondPage.HasMore || secondPage.NextBeforeID != 0 {
		t.Fatalf("second page cursor = (%t, %d)", secondPage.HasMore, secondPage.NextBeforeID)
	}
}

func TestAuditStoreListReturnsEmptyPage(t *testing.T) {
	page, err := openTestAuditStore(t).List(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 || page.HasMore || page.NextBeforeID != 0 {
		t.Fatalf("empty page = %#v", page)
	}
}

func TestAuditStoreListValidatesBounds(t *testing.T) {
	store := openTestAuditStore(t)
	for _, test := range []struct {
		name     string
		beforeID int64
		limit    int
	}{
		{name: "negative cursor", beforeID: -1, limit: 50},
		{name: "zero limit", limit: 0},
		{name: "limit too large", limit: 101},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.List(context.Background(), test.beforeID, test.limit); err == nil {
				t.Fatal("List() error = nil, want validation error")
			}
		})
	}
}

func TestAuditStoreListRejectsInvalidStoredTimestamp(t *testing.T) {
	store := openTestAuditStore(t)
	if _, err := store.db.Exec(
		"INSERT INTO audit_events (actor, action, outcome, created_at) VALUES (?, ?, ?, ?)",
		"admin", "auth.login", "success", "not-a-timestamp",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), 0, 50); err == nil || !strings.Contains(err.Error(), "parse audit event timestamp") {
		t.Fatalf("List() error = %v, want timestamp context", err)
	}
}

func TestAuditStoreListHonorsCancellationAndClosedStore(t *testing.T) {
	store := openTestAuditStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.List(ctx, 0, 50); err == nil || !strings.Contains(err.Error(), "list audit events") {
		t.Fatalf("cancelled List() error = %v", err)
	}

	closedStore, err := OpenAuditStore(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closedStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closedStore.List(context.Background(), 0, 50); err == nil || !strings.Contains(err.Error(), "list audit events") {
		t.Fatalf("closed List() error = %v", err)
	}
}

func openTestAuditStore(t *testing.T) *AuditStore {
	t.Helper()

	store, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("OpenAuditStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}
