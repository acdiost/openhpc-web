package slurmconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalProviderListsRegularConfigurationFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "slurm.conf"), []byte("ClusterName=cluster\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gres.conf"), []byte("NodeName=node01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "slurm.conf"), filepath.Join(root, "linked.conf")); err != nil {
		t.Fatal(err)
	}

	provider, err := New(root, 1024)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	files, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(files) != 2 || files[0].Name != "gres.conf" || files[1].Name != "slurm.conf" {
		t.Fatalf("files = %#v, want sorted regular files", files)
	}
}

func TestLocalProviderListsConfigurationFilesRepeatedly(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"gres.conf", "slurm.conf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("ClusterName=cluster\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	provider, err := New(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	for attempt := range 2 {
		files, err := provider.List(context.Background())
		if err != nil {
			t.Fatalf("List() attempt %d error = %v", attempt+1, err)
		}
		if len(files) != 2 || files[0].Name != "gres.conf" || files[1].Name != "slurm.conf" {
			t.Fatalf("List() attempt %d = %#v, want both files", attempt+1, files)
		}
	}
}

func TestSelectConfigEntriesFiltersAndSortsConfigurationFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"slurm.conf", "gres.conf", "ignored file", "directory"} {
		path := filepath.Join(root, name)
		if name == "directory" {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "slurm.conf"), filepath.Join(root, "linked.conf")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	files := selectConfigEntries(entries)
	if len(files) != 2 || files[0].Name != "gres.conf" || files[1].Name != "slurm.conf" {
		t.Fatalf("selectConfigEntries() = %#v, want sorted regular configuration files", files)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"slurm.conf":    true,
		"../slurm.conf": false,
		"bad name":      false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLocalProviderReadsAndTruncatesConfiguration(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "slurm.conf"), []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := New(root, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	file, err := provider.Read(context.Background(), "slurm.conf")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if file.Content != "12345" || !file.Truncated || file.Size != 10 {
		t.Fatalf("file = %#v, want bounded content", file)
	}
}

func TestLocalProviderRedactsSensitiveConfigurationValues(t *testing.T) {
	root := t.TempDir()
	content := "AccountingStoragePass=super-secret\nAuthInfo=token-value\nPrivateKey=/secret/key\nClusterName=hpc\n"
	if err := os.WriteFile(filepath.Join(root, "slurm.conf"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := New(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	file, err := provider.Read(context.Background(), "slurm.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"super-secret", "token-value", "/secret/key"} {
		if strings.Contains(file.Content, secret) {
			t.Errorf("content contains unredacted secret %q: %q", secret, file.Content)
		}
	}
	if !strings.Contains(file.Content, "REDACTED") || !strings.Contains(file.Content, "ClusterName=hpc") {
		t.Errorf("redacted content = %q", file.Content)
	}
}

func TestLocalProviderRejectsUnsafeNamesAndSymlinkReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "slurm.conf"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "slurm.conf"), filepath.Join(root, "linked.conf")); err != nil {
		t.Fatal(err)
	}
	provider, err := New(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	for _, name := range []string{"../slurm.conf", "/etc/passwd", "nested/slurm.conf", "linked.conf", "", "bad name"} {
		if _, err := provider.Read(context.Background(), name); err == nil {
			t.Errorf("Read(%q) error = nil, want rejection", name)
		}
	}
	if _, err := provider.Read(context.Background(), "slurm.conf"); err != nil {
		t.Fatalf("Read(slurm.conf) error = %v", err)
	}
}

func TestLocalProviderRejectsUnsafeRoots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"relative", file, link, filepath.Join(t.TempDir(), "missing")} {
		if provider, err := New(root, 1024); err == nil {
			_ = provider.Close()
			t.Errorf("New(%q) error = nil, want rejection", root)
		}
	}
}
