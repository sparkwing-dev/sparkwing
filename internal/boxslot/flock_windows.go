//go:build windows

package boxslot

import (
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx mirrors POSIX flock's per-process advisory-exclusive
// semantics closely enough for the boxslot scheme: the lock is
// released when the file handle closes (process exit included), and
// LOCKFILE_FAIL_IMMEDIATELY gives the non-blocking variant.
//
// We lock the full file extent so adding a future shared-lock variant
// over a different byte range stays a clean extension.

const lockBytes = 1 << 30

func flockExclusive(f *os.File) error {
	return lockFile(f, 0)
}

func flockExclusiveNonblock(f *os.File) error {
	return lockFile(f, windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func flockUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockBytes, 0, &ol)
}

func lockFile(f *os.File, flags uint32) error {
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|flags,
		0,
		lockBytes,
		0,
		&ol,
	)
}
