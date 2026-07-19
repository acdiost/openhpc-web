package platform

import (
	"context"
	"errors"
	"testing"
)

func TestUserStoreRoundTripAndEnable(t *testing.T) {
	store, err := OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user := PlatformUser{Username: "alice", PasswordHash: "hash", Role: RoleUser, Enabled: true}
	if err := store.Upsert(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("Get() = %#v, %v, %v", got, found, err)
	}
	if got.Role != RoleUser || !got.Enabled || got.PasswordHash != "hash" {
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
