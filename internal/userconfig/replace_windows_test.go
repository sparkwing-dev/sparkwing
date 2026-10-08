//go:build windows

package userconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestWindowsConfigProducerWritesPrivateConfigWithoutTempResidue(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, Filename)
	t.Setenv(PathEnv, path)
	for _, paths := range [][]string{{"first"}, {"second"}} {
		if err := Write(path, Repos, "repo registry", map[string]any{"fallback_paths": paths}); err != nil {
			t.Fatal(err)
		}
		file, err := fssecure.OpenPrivateConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				t.Fatalf("temporary config directory survives publication: %s", entry.Name())
			}
		}
	}
}
