//go:build windows

package boxslot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsLockExcludesLegacyRangeInBothDirections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holder.lock")
	legacy, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.WriteString("metadata"); err != nil {
		t.Fatal(err)
	}
	current, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	legacyLock := func() error {
		var overlap windows.Overlapped
		return windows.LockFileEx(windows.Handle(legacy.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1<<30, 0, &overlap)
	}
	if err := legacyLock(); err != nil {
		t.Fatal(err)
	}
	if err := flockExclusiveNonblock(current); !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("current lock with legacy holder = %v", err)
	}
	var overlap windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(legacy.Fd()), 0, 1<<30, 0, &overlap); err != nil {
		t.Fatal(err)
	}
	if err := flockExclusiveNonblock(current); err != nil {
		t.Fatal(err)
	}
	defer flockUnlock(current)
	if err := legacyLock(); !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("legacy lock with current holder = %v", err)
	}
	body := make([]byte, len("metadata"))
	if _, err := legacy.ReadAt(body, 0); err != nil || string(body) != "metadata" {
		t.Fatalf("metadata read under current lock = %q, %v", body, err)
	}
}
