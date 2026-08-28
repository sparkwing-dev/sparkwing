//go:build windows

package sparkwing

import (
	"os"

	"golang.org/x/sys/windows"
)

const lintSlotLockBytes = 1 << 30

func flockExclusiveNonblock(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		lintSlotLockBytes,
		0,
		&ol,
	)
}

func flockUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lintSlotLockBytes, 0, &ol)
}
