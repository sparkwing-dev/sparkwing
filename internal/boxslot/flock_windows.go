//go:build windows

package boxslot

import (
	"os"

	"golang.org/x/sys/windows"
)

// safety: the last byte overlaps legacy locks while leaving metadata readable.
const (
	lockOffset = (1 << 30) - 1
	lockBytes  = 1
)

func flockExclusive(f *os.File) error {
	return lockFile(f, 0)
}

func flockExclusiveNonblock(f *os.File) error {
	return lockFile(f, windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func flockUnlock(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytes, 0, &ol)
}

func lockFile(f *os.File, flags uint32) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|flags,
		0,
		lockBytes,
		0,
		&ol,
	)
}
