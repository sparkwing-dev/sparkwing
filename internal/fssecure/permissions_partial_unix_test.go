//go:build !windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRepairOpenedTreeReturnsChangesMadeBeforeReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	changes, err := repairOpenedTree(f, stat, ".", false)
	if err == nil {
		t.Fatal("repairOpenedTree unexpectedly read a regular file as a directory")
	}
	if len(changes) != 1 || changes[0].Before != "0755" || changes[0].After != "0700" {
		t.Fatalf("partial changes = %+v", changes)
	}
}
