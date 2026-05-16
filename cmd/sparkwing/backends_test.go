package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestWriteProfileBackendsConfig_RoundTripsThroughOverlay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	logDir := t.TempDir()
	cacheDir := t.TempDir()

	path, cleanup, err := writeProfileBackendsConfig(
		"fs://"+logDir,
		"fs://"+cacheDir,
	)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if path == "" {
		t.Fatal("expected a temp path")
	}
	defer cleanup()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tmp: %v", err)
	}
	if !strings.Contains(string(body), "type: filesystem") {
		t.Errorf("rendered yaml missing type: %s", body)
	}

	// Confirm the inner backends.Resolve picks it up via the overlay.
	repoDir := filepath.Join(t.TempDir(), ".sparkwing")
	file, err := backends.ResolveWithEnvAndOverlay(repoDir, path)
	if err != nil {
		t.Fatalf("resolve overlay: %v", err)
	}
	if file.Defaults.Logs == nil || file.Defaults.Logs.Path != logDir {
		t.Errorf("logs not picked up: %+v", file.Defaults.Logs)
	}
	if file.Defaults.Cache == nil || file.Defaults.Cache.Path != cacheDir {
		t.Errorf("cache not picked up: %+v", file.Defaults.Cache)
	}

	// And that the factory builds a real store from the resolved spec.
	if _, err := storeurl.OpenLogStoreFromSpec(context.Background(), *file.Defaults.Logs); err != nil {
		t.Errorf("OpenLogStoreFromSpec: %v", err)
	}
}

func TestWriteProfileBackendsConfig_NoopWhenProfileEmpty(t *testing.T) {
	path, _, err := writeProfileBackendsConfig("", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
}

func TestWriteProfileBackendsConfig_RejectsBadURL(t *testing.T) {
	_, _, err := writeProfileBackendsConfig("not-a-url", "")
	if err == nil {
		t.Fatal("expected error")
	}
}
