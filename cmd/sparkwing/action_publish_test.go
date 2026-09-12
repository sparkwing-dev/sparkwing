package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePublishProfiles(t *testing.T, cachePath string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := "profiles:\n" +
		"  team:\n" +
		"    secrets:\n      type: none\n" +
		"    state:\n      type: sqlite\n      path: " + filepath.Join(t.TempDir(), "state.db") + "\n" +
		"    cache:\n      type: filesystem\n      path: " + cachePath + "\n" +
		"    logs:\n      type: stdout\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", path)
}

func TestResolveArtifactStoreReadsTheNamedProfile(t *testing.T) {
	cache := t.TempDir()
	writePublishProfiles(t, cache)

	store, location, err := resolveArtifactStore(context.Background(), "team", "")
	if err != nil {
		t.Fatalf("resolveArtifactStore: %v", err)
	}
	if store == nil {
		t.Fatal("resolveArtifactStore returned no store")
	}
	if !strings.Contains(location, cache) {
		t.Fatalf("location %q does not name the profile's cache path %q", location, cache)
	}
}

func TestResolveArtifactStorePrefersTheExplicitURL(t *testing.T) {
	writePublishProfiles(t, t.TempDir())

	url := "fs://" + t.TempDir()
	_, location, err := resolveArtifactStore(context.Background(), "team", url)
	if err != nil {
		t.Fatalf("resolveArtifactStore: %v", err)
	}
	if location != url {
		t.Fatalf("location = %q, want %q", location, url)
	}
}

func TestResolveArtifactStoreRefusesWithNeitherSource(t *testing.T) {
	_, _, err := resolveArtifactStore(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected a refusal with no profile and no URL")
	}
	if strings.Contains(err.Error(), "artifact_store") {
		t.Fatalf("refusal names a profile field that no longer exists: %v", err)
	}
}
