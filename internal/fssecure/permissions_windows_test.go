//go:build windows

package fssecure_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestWindowsReportsPOSIXPermissionAuditUnsupported(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := fssecure.EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	if fssecure.AuditSupported() {
		t.Fatal("POSIX permission audit reported supported on Windows")
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := fssecure.RepairTree(root, info, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("Windows POSIX permission repair reported changes: %+v", changes)
	}
	f, err := fssecure.OpenFile(filepath.Join(root, "state"), os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
