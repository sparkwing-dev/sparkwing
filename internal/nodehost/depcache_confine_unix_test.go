//go:build !windows

package nodehost

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

func TestDeclaredCachePathStaysInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	for _, declared := range []string{"/etc/sparkwing/credentials", "../outside"} {
		if parent, _, err := (directoryCache{workspace: workspace, declared: declared}).open(); err == nil {
			parent.Close()
			t.Errorf("declared cache %q opened", declared)
		}
	}
}

// The job runs between the host's restore and its save, so it can swap the declared
// directory, or a component above it, for a symlink to a file only the host can read.
func TestCacheSaveAndRestoreRefuseASymlinkSwappedIn(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "agent-token"), []byte("runner-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "vendor", "deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "vendor", "deps", "module"), []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, declared := range []string{"vendor/deps", "vendor"} {
		cache := directoryCache{workspace: workspace, declared: "vendor/deps", dir: filepath.Join(workspace, "vendor", "deps")}
		var archive bytes.Buffer
		if err := writeCacheArchive(t.Context(), &archive, cache); err != nil {
			t.Fatalf("save before the swap: %v", err)
		}
		swapped := filepath.Join(workspace, declared)
		moved := swapped + ".moved"
		if err := os.Rename(swapped, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, swapped); err != nil {
			t.Fatal(err)
		}
		if err := writeCacheArchive(t.Context(), &bytes.Buffer{}, cache); err == nil {
			t.Errorf("save followed a symlinked %s outside the workspace", declared)
		}
		parent, name, err := cache.open()
		if err != nil {
			t.Fatal(err)
		}
		err = tarsafe.ExtractIntoRootStaged(parent, name, ".restore-", func(*os.Root) error { return nil })
		parent.Close()
		if declared == "vendor" && err == nil {
			t.Errorf("restore followed a symlinked %s outside the workspace", declared)
		}
		if declared == "vendor/deps" {
			info, statErr := os.Lstat(swapped)
			if err != nil || statErr != nil || !info.IsDir() {
				t.Errorf("restore did not replace the swapped-in symlink with a directory: %v %v", err, statErr)
			}
			if err := os.RemoveAll(swapped); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, swapped); err != nil {
				t.Fatal(err)
			}
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 1 {
			t.Fatalf("restore wrote outside the workspace: %v %v", entries, err)
		}
		if body, err := os.ReadFile(filepath.Join(outside, "agent-token")); err != nil || string(body) != "runner-credential" {
			t.Fatalf("restore changed a file outside the workspace: %q %v", body, err)
		}
		if err := os.Remove(swapped); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, swapped); err != nil {
			t.Fatal(err)
		}
	}
}
