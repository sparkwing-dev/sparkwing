package sparkwing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestSaveLintCachePreservesDirectoryInspectionFailure(t *testing.T) {
	useWorkDir(t, t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	cache := sparkwing.ToolCacheDir("golangci-lint")
	parent := filepath.Dir(cache)
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := sparkwing.SaveLintCache(context.Background(), "http://unused.invalid", "")
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("save error = %v, want ENOTDIR", err)
	}
}
