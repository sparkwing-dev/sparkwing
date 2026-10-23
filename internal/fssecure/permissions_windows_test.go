//go:build windows

package fssecure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsReportsPOSIXPermissionAuditUnsupported(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	if auditSupported {
		t.Fatal("POSIX permission audit reported supported on Windows")
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := repairTree(root, info, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("Windows POSIX permission repair reported changes: %+v", changes)
	}
	f, err := OpenFile(filepath.Join(root, "state"), os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
