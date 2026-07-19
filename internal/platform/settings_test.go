package platform

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSettingsStorePersistsAndRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	key := []byte("0123456789abcdef0123456789abcdef")
	store, err := OpenSettingsStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_URL", "ldaps://ldap.example.com:636"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD", "secret<&"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Value == "secret<&" || !entries[1].Configured {
		t.Fatalf("entries = %#v", entries)
	}
	value, found, err := store.Get(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD")
	if err != nil || !found || value != "secret<&" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSettingsStore(path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	value, found, err = store.Get(context.Background(), "OPENHPC_LDAP_URL")
	if err != nil || !found || value != "ldaps://ldap.example.com:636" {
		t.Fatalf("reopened Get() = %q, %v, %v", value, found, err)
	}
}

func TestSettingsStoreRequiresKeyForSecretsAndClearsValues(t *testing.T) {
	store, err := OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Set(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD", "secret"); !errors.Is(err, ErrSettingsKeyRequired) {
		t.Fatalf("Set(secret) error = %v", err)
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_URL", "value"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_URL", ""); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(context.Background(), "OPENHPC_LDAP_URL"); err != nil || found {
		t.Fatalf("cleared setting = found:%v err:%v", found, err)
	}
	keyed, err := OpenSettingsStore(":memory:", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	defer keyed.Close()
	if err := keyed.Set(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := keyed.Set(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD", ""); err != nil {
		t.Fatal(err)
	}
	if _, found, err := keyed.Get(context.Background(), "OPENHPC_LDAP_BIND_PASSWORD"); err != nil || found {
		t.Fatalf("cleared secret = found:%v err:%v", found, err)
	}
}

func TestSettingsStoreRejectsUnknownKeysAndOversizedValues(t *testing.T) {
	store, err := OpenSettingsStore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, key := range []string{"", "OPENHPC_LDAP_URL'", " OPENHPC_LDAP_URL"} {
		if err := store.Set(context.Background(), key, "value"); err == nil {
			t.Errorf("Set(%q) error = nil", key)
		}
	}
	if err := store.Set(context.Background(), "OPENHPC_LDAP_URL", string(make([]byte, maxSettingValueBytes+1))); err == nil {
		t.Error("Set(oversized) error = nil")
	}
}
