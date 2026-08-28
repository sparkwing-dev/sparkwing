package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func fsCacheProfile(dir string) *profile.Profile {
	return &profile.Profile{
		Name:  "test",
		Cache: &backends.Spec{Type: backends.TypeFilesystem, Path: dir},
	}
}

func TestCoordinatedArtifactStore_ProfileWinsOverExplicitEnv(t *testing.T) {
	profileDir := t.TempDir()
	envDir := t.TempDir()
	t.Setenv(ArtifactStoreEnvVar, "fs://"+envDir)

	art, err := coordinatedArtifactStore(context.Background(), fsCacheProfile(profileDir))
	if err != nil {
		t.Fatalf("coordinatedArtifactStore: %v", err)
	}
	if art == nil {
		t.Fatal("no artifact store opened for a profile that declares a cache surface")
	}
	if err := art.Put(context.Background(), "probe", strings.NewReader("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !dirHasContent(t, profileDir) {
		t.Error("the profile's cache directory received nothing")
	}
	if dirHasContent(t, envDir) {
		t.Error("the node published into SPARKWING_CACHE_URL while its profile named another store")
	}
}

func TestCoordinatedArtifactStore_ExplicitEnvFillsInWithoutAProfileCache(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv(ArtifactStoreEnvVar, "fs://"+envDir)

	art, err := coordinatedArtifactStore(context.Background(), nil)
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

	art, err := coordinatedArtifactStore(context.Background(), nil)
	if err != nil {
		t.Fatalf("coordinatedArtifactStore: %v", err)
	}
	if art != nil {
		t.Fatal("the child opened the cache store named in dev.env")
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
