//go:build windows

package fs

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const casLockBytes = 1 << 30

func lockExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, casLockBytes, 0, &overlapped)
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		return errLockUnsupported
	}
	return err
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, casLockBytes, 0, &overlapped)
}
