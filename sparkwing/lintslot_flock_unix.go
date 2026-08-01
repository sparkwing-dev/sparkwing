//go:build !windows

package sparkwing

import (
	"os"
	"syscall"
)

// flockExclusiveNonblock takes the slot lock, or reports that someone
// else holds it rather than waiting. The kernel drops the lock when the
// holding process exits, including on a crash, so a slot cannot leak.
func flockExclusiveNonblock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
