//go:build windows

package userconfig

import (
	"os"

	"golang.org/x/sys/windows"
)

const configLockBytes = 1 << 30

func flockWait(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, configLockBytes, 0, &ol)
}

func flockUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, configLockBytes, 0, &ol)
}
