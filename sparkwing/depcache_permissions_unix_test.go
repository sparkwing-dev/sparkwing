//go:build !windows

package sparkwing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDepCacheCreatesPrivateArchive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.sum"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &localDepCache{}
	if _, err := backend.store(context.Background(), "private-cache", source); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(home, "depcache"):                         0o700,
		filepath.Join(home, "depcache", "private-cache.tar.gz"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
