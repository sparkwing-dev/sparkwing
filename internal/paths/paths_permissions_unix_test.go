//go:build !windows

package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestEnsureRootAndRunDirArePrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	p := paths.PathsAt(root)
	if err := os.MkdirAll(p.RunDir("run-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, p.RunsDir(), p.RunDir("run-1")} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureRunDir("run-1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, p.RunsDir(), p.RunDir("run-1")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s mode = %04o, want 0700", path, got)
		}
	}
}
