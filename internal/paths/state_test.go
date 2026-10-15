package paths

import (
	"errors"
	"os"
	"testing"
)

func TestNewPathsLeavesStateDatabaseAbsent(t *testing.T) {
	p := PathsAt(t.TempDir())
	if err := p.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state database exists: %v", err)
	}
}
