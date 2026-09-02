//go:build !windows

package profile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func TestSaveKeepsTheConfigDirectoryAndFilePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sparkwing")
	path := filepath.Join(dir, "profiles.yaml")
	cfg := &profile.Config{Profiles: map[string]*profile.Profile{
		"prod": {Controller: &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_secret"}},
	}}
	if err := profile.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("config directory mode = %#o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("profiles.yaml mode = %#o, want 0600", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "profiles.yaml" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("config directory holds %v, want only profiles.yaml", names)
	}
}

func TestSaveTightensAWorldReadableConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	path := filepath.Join(dir, "profiles.yaml")
	if err := profile.Save(path, &profile.Config{Profiles: map[string]*profile.Profile{}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("config directory mode = %#o, want 0700", got)
	}
}
