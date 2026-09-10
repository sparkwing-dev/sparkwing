package bincache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func lockedParent(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not deny mkdir on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("mkdir locked: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	return locked
}

func TestCompilePipelineNamesTheHomeOverride(t *testing.T) {
	locked := lockedParent(t)
	dest := filepath.Join(locked, "cache", "pipelines", "deadbeef", "pipeline")

	err := CompilePipeline(context.Background(), t.TempDir(), dest)
	if err == nil {
		t.Fatal("expected CompilePipeline to fail on an unwritable cache parent")
	}
	got := err.Error()
	if !strings.Contains(got, filepath.Join(locked, "cache")) {
		t.Errorf("error does not name the directory it could not create: %s", got)
	}
	if !strings.Contains(got, "SPARKWING_HOME") {
		t.Errorf("error names no supported way to choose another location: %s", got)
	}
}

func TestMkdirCacheKeepsTheUnderlyingError(t *testing.T) {
	dir := filepath.Join(lockedParent(t), "cache", "bin")

	err := mkdirCache(dir)
	if err == nil {
		t.Fatal("expected mkdirCache to fail on an unwritable parent")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("wrapped error no longer reports a permission problem: %v", err)
	}
	if !strings.Contains(err.Error(), "SPARKWING_HOME") {
		t.Errorf("error names no supported way to choose another location: %v", err)
	}
}
