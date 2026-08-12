//go:build windows

package orchestrator

import (
	"os"

	"golang.org/x/sys/windows"
)

// consumerLockBytes is the byte range locked on the consumer lock file.
// Windows locks ranges rather than whole files; a range wider than the
// (empty) lock file is the standard way to emulate flock.
const consumerLockBytes = 1 << 30

// flockTry takes a non-blocking exclusive lock on f. ok is false when
// another process holds it. Windows releases the lock when the handle
// closes, including on abnormal termination, matching the Unix build's
// crash semantics.
func flockTry(f *os.File) (ok bool, err error) {
	var ol windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, consumerLockBytes, 0, &ol,
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
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, consumerLockBytes, 0, &ol)
}
