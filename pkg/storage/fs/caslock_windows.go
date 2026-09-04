//go:build windows

package fs

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const casLockBytes = 1 << 30

func tryLockExclusive(f *os.File) (bool, error) {
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, casLockBytes, 0, &overlapped)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	case errors.Is(err, windows.ERROR_INVALID_FUNCTION), errors.Is(err, windows.ERROR_NOT_SUPPORTED):
		return false, errLockUnsupported
	}
	return false, err
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, casLockBytes, 0, &overlapped)
}
