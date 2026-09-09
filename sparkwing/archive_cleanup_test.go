package sparkwing

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStagedExtractionPreservesCleanupFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	rejected := errors.New("archive rejected")
	err := extractIntoDirStaged(filepath.Join(parent, "cache"), "stage-*", func(string) error {
		if err := os.RemoveAll(parent); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("extract error = %v, want rejection and cleanup ENOTDIR", err)
	}
}
