package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoordinatedArtifactStore_ExplicitEnvFillsIn(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv(ArtifactStoreEnvVar, "fs://"+envDir)

	art, err := coordinatedArtifactStore(context.Background())
	if err != nil {
		t.Fatalf("coordinatedArtifactStore: %v", err)
	}
	if art == nil {
		t.Fatal("an explicit SPARKWING_CACHE_URL was ignored")
	}
	if err := art.Put(context.Background(), "probe", strings.NewReader("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !dirHasContent(t, envDir) {
		t.Error("the explicit cache directory received nothing")
	}
}

func TestCoordinatedArtifactStore_NeverReadsDevEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv(ArtifactStoreEnvVar, "")
	dashboardDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "dev.env"),
		[]byte(ArtifactStoreEnvVar+"=fs://"+dashboardDir+"\n"), 0o644); err != nil {
		t.Fatalf("write dev.env: %v", err)
	}

	art, err := coordinatedArtifactStore(context.Background())
	if err != nil {
		t.Fatalf("coordinatedArtifactStore: %v", err)
	}
	if art == nil {
		t.Fatal("no local artifact cache")
	}
	if err := art.Put(t.Context(), "probe", strings.NewReader("local")); err != nil {
		t.Fatal(err)
	}
	if dirHasContent(t, dashboardDir) {
		t.Fatal("the child opened the cache store named in dev.env")
	}
	if !dirHasContent(t, filepath.Join(home, "cache")) {
		t.Fatal("local cache received nothing")
	}
}

func dirHasContent(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

func TestResolveArtifactStoreFromEnv_OpensURL(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "fs://"+t.TempDir())
	s, err := resolveArtifactStoreFromEnv(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if s == nil {
		t.Fatal("expected a store for a set fs:// URL")
	}
}

func TestResolveArtifactStoreFromEnv_BadURLErrors(t *testing.T) {
	t.Setenv(ArtifactStoreEnvVar, "nonsense://x")
	if _, err := resolveArtifactStoreFromEnv(context.Background()); err == nil {
		t.Fatal("expected an error for an unsupported scheme")
	}
}
