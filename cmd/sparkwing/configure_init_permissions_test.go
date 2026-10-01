//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestConfigureInitTightensAndReportsAnExistingConfigDirectory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_CONFIG", "")
	dir := filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	profiles := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(profiles, []byte("profiles: {}\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", profiles, err)
	}
	if err := os.Chmod(profiles, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", profiles, err)
	}

	info, err := gatherConfigureInit(false)
	if err != nil {
		t.Fatalf("gatherConfigureInit: %v", err)
	}
	if info.Created {
		t.Error("reported the config directory as created when it already existed")
	}
	if info.Mode != "0700" {
		t.Errorf("config directory mode = %q, want 0700", info.Mode)
	}
	if got := statMode(t, dir); got != 0o700 {
		t.Errorf("config directory left at %#o, want 0700", got)
	}
	found := false
	for _, f := range info.ConfigFiles {
		if f.Name != "config.yaml" {
			continue
		}
		found = true
		if f.Mode != "0644" {
			t.Errorf("config.yaml mode = %q, want 0644", f.Mode)
		}
		if !f.Exposed {
			t.Error("config.yaml at 0644 was not flagged as group- or other-readable")
		}
	}
	if !found {
		t.Fatal("config.yaml missing from the survey")
	}
}

func TestConfigureInitDryRunLeavesTheDirectoryAlone(t *testing.T) {
	// safety: Go's telemetry sidecar can outlive the version probe and race TempDir cleanup.
	telemetry := t.TempDir()
	if err := os.WriteFile(filepath.Join(telemetry, "mode"), []byte("off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_TELEMETRY_DIR", telemetry)

	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_CONFIG", "")
	dir := filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	info, err := gatherConfigureInit(true)
	if err != nil {
		t.Fatalf("gatherConfigureInit: %v", err)
	}
	if got := statMode(t, dir); got != 0o755 {
		t.Errorf("--dry-run changed the mode to %#o, want 0755", got)
	}
	if !info.Exposed {
		t.Error("--dry-run did not flag the group- and other-readable config directory")
	}
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestConfigureInitDryRunWritesNothing(t *testing.T) {
	if err := os.MkdirAll(paths.TestSandbox(), 0o700); err != nil {
		t.Fatal(err)
	}
	xdg, err := os.MkdirTemp(paths.TestSandbox(), "xdg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SPARKWING_CONFIG", "")
	t.Setenv("SPARKWING_HOME", "")
	dir := filepath.Join(xdg, "sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.yaml"), []byte("profiles:\n  prod:\n    controller: {url: https://api.example}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := gatherConfigureInit(true); err != nil {
		t.Fatalf("gatherConfigureInit --dry-run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote config.yaml: %v", err)
	}
	if _, err := gatherConfigureInit(false); err != nil {
		t.Fatalf("gatherConfigureInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatalf("config.yaml after a real run: %v", err)
	}
}
