//go:build windows

package sparkwing

import (
	"os"

	"golang.org/x/sys/windows"
)

// lintSlotLockBytes is the range LockFileEx covers. The lock file holds
// no data, so any range works as long as every locker names the same
// one.
const lintSlotLockBytes = 1 << 30

// flockExclusiveNonblock takes the slot lock, or reports that someone
// else holds it rather than waiting. Windows releases the range when
// the handle closes, including on a crash, so a slot cannot leak.
//
// A slot needs a symlink as well as a lock, and an unprivileged Windows
// process cannot make one. [AcquireLintSlot] falls back to a private
// per-worktree cache when that fails, so this path stays correct and
// simply does not get the shared cache.
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
