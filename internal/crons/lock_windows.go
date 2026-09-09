//go:build windows

package crons

import (
	"os"

	"golang.org/x/sys/windows"
)

const tickLockBytes = 1 << 30

func flockExclusiveNonblock(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, tickLockBytes, 0, &ol,
	)
	if err == nil {
		return true, nil
	}
	if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING { //nolint:errorlint // raw Windows errno
		return false, nil
	}
	return false, err
}

func flockUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, tickLockBytes, 0, &ol)
}
