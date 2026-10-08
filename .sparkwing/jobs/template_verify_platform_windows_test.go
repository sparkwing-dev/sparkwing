//go:build windows

package jobs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTemplateVerifyWindowsCLIIsDiscoverable(t *testing.T) {
	path := templateVerifyCLIPath(t.TempDir(), "windows")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := exec.LookPath(path); err != nil || got != path {
		t.Fatalf("look up Windows CLI = %q, %v; want %q", got, err, path)
	}
}

func TestTemplateVerifyWindowsLockExcludesAnotherHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verify.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := lockTemplateVerifyFile(first); err != nil {
		t.Fatal(err)
	}
	var overlapped windows.Overlapped
	try := func() error {
		return windows.LockFileEx(windows.Handle(second.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	}
	if err := try(); !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		t.Fatalf("second handle lock = %v; want lock violation", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := try(); err != nil {
		t.Fatalf("lock after owner closes: %v", err)
	}
}

func TestTemplateVerifyWindowsDiskSpace(t *testing.T) {
	root := t.TempDir()
	free, err := availableTemplateVerifyDisk(root)
	if err != nil || free == 0 {
		t.Fatalf("available disk = %d, %v", free, err)
	}
	if _, err := availableTemplateVerifyDisk(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing filesystem path should fail")
	}
}
