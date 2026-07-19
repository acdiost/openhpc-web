package platform

import (
	"context"
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
