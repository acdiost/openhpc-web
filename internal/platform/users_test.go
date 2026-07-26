package platform

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUserStoreRoundTripAndEnable(t *testing.T) {
	store, err := OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user := PlatformUser{Username: "alice", PasswordHash: "hash", Role: RoleUser, Phone: "+86 13800000000", Organization: "Research Lab", Email: "alice@example.com", Enabled: true}
	if err := store.Upsert(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	if got.Role != RoleUser || !got.Enabled || got.PasswordHash != "hash" || got.Phone != user.Phone || got.Organization != user.Organization || got.Email != user.Email {
		t.Fatalf("unexpected user: %#v", got)
	}
	if err := store.SetEnabled(context.Background(), "alice", false); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.Get(context.Background(), "alice")
	if err != nil || got.Enabled {
		t.Fatalf("disabled user = %#v, %v", got, err)
	}
}

func TestValidatePlatformUserInput(t *testing.T) {
	for _, username := range []string{"", "../root", "a/b", " space"} {
		if err := ValidateUsername(username); err == nil {
			t.Errorf("ValidateUsername(%q) accepted", username)
		}
	}
	if err := ValidateRole("owner"); err == nil {
		t.Error("ValidateRole accepted unsupported role")
	}
	if err := ValidateUserProfile("", "", ""); err != nil {
		t.Errorf("ValidateUserProfile rejected optional empty fields: %v", err)
	}
	if err := ValidateUserProfile("", "", "not-an-email"); err == nil {
		t.Error("ValidateUserProfile accepted invalid email")
	}
}

func TestOpenUserStoreMigratesOptionalProfileColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE platform_users (
		username TEXT PRIMARY KEY, password_hash TEXT NOT NULL, role TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO platform_users(username,password_hash,role,enabled,created_at) VALUES('legacy','hash','user',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, found, err := store.Get(context.Background(), "legacy")
	if err != nil || !found || got.Phone != "" || got.Organization != "" || got.Email != "" {
		t.Fatalf("migrated user = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestUserStoreCreateRejectsDuplicateWithoutOverwritingExistingUser(t *testing.T) {
	store, err := OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	original := PlatformUser{Username: "alice", PasswordHash: "original-hash", Role: RoleUser, Enabled: true}
	if err := store.Create(context.Background(), original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Create(context.Background(), PlatformUser{Username: "alice", PasswordHash: "replacement-hash", Role: RoleAdmin, Enabled: false}); !errors.Is(err, ErrUserExists) {
		t.Fatalf("Create() error = %v, want ErrUserExists", err)
	}

	got, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	if got.PasswordHash != original.PasswordHash || got.Role != original.Role || got.Enabled != original.Enabled {
		t.Fatalf("duplicate Create() overwrote user: %#v", got)
	}
}
