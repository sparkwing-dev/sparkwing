package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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
	if location != "fs://"+cache {
		t.Fatalf("location = %q, want the fs:// URL for %q", location, cache)
	}
	if _, err := storeurl.OpenArtifactStore(context.Background(), location); err != nil {
		t.Fatalf("the reported location is not one --artifact-store accepts: %v", err)
	}
}

func TestResolveArtifactStoreReportsAnS3ProfileAsAnS3URL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	body := "profiles:\n" +
		"  team:\n" +
		"    secrets:\n      type: none\n" +
		"    state:\n      type: sqlite\n      path: " + filepath.Join(t.TempDir(), "state.db") + "\n" +
		"    cache:\n      type: filesystem\n      path: " + t.TempDir() + "\n" +
		"      binaries:\n        type: s3\n        bucket: team-binaries\n        prefix: /pipelines/\n" +
		"    logs:\n      type: stdout\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_PROFILES", path)

	p, err := resolveProfile("team")
	if err != nil {
		t.Fatalf("resolveProfile: %v", err)
	}
	location, err := artifactStoreURL(p, *p.Surfaces().BinaryCache())
	if err != nil {
		t.Fatalf("artifactStoreURL: %v", err)
	}
	if location != "s3://team-binaries/pipelines" {
		t.Fatalf("location = %q", location)
	}
}

func TestArtifactStoreURLReportsARelativeCachePathAsOneTheFlagAccepts(t *testing.T) {
	writePublishProfiles(t, "cache-dir")
	p, err := resolveProfile("team")
	if err != nil {
		t.Fatalf("resolveProfile: %v", err)
	}

	location, err := artifactStoreURL(p, *p.Surfaces().BinaryCache())
	if err != nil {
		t.Fatalf("artifactStoreURL: %v", err)
	}

	if !strings.HasPrefix(location, "fs:///") {
		t.Fatalf("location = %q, want an absolute fs:// URL", location)
	}
	if _, err := storeurl.OpenArtifactStore(context.Background(), location); err != nil {
		t.Fatalf("the reported location is not one --artifact-store accepts: %v", err)
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
