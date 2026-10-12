//go:build windows

package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRefWorktreeLeaseSupportsLongPathsAndRemovalWhileLocked(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-path-", 12), strings.Repeat("long-path-", 12))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run.lease")
	held, err := openRefWorktreeLease(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	locked, err := flockTry(held)
	if err != nil || !locked {
		t.Fatalf("lease lock = %v, %v", locked, err)
	}
	defer flockUnlock(held)
	contender, err := openRefWorktreeLease(path)
	if err != nil {
		t.Fatal(err)
	}
	locked, err = flockTry(contender)
	contender.Close()
	if err != nil || locked {
		t.Fatalf("contender lock = %v, %v", locked, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove held lease: %v", err)
	}
}
