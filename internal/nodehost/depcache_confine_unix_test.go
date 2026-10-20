//go:build !windows

package nodehost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredCachePathStaysInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "real", "deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "real", "swapped")); err != nil {
		t.Fatal(err)
	}
	for _, declared := range []string{"/etc/sparkwing/credentials", "../outside", "linked", "linked/deps", "real/swapped"} {
		if dir, err := (directoryCache{workspace: workspace, declared: declared}).confined(); err == nil {
			t.Errorf("declared cache %q resolved to %q", declared, dir)
		}
	}
	for _, declared := range []string{"real/deps", "node_modules"} {
		if dir, err := (directoryCache{workspace: workspace, declared: declared}).confined(); err != nil || dir != filepath.Join(workspace, declared) {
			t.Errorf("declared cache %q = %q, %v", declared, dir, err)
		}
	}
}
