//go:build !windows

package orchestrator

import (
	"os"
	"syscall"
)

// flockTry takes a non-blocking exclusive lock on f. ok is false when
// another process holds it. The kernel drops the lock when the holder's
// descriptor closes -- including on crash or SIGKILL -- which is what
// makes a free lock proof that no consumer is alive, with no stale-PID
// heuristics.
func flockTry(f *os.File) (ok bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK { //nolint:errorlint // raw errno from Flock
		return false, nil
	}
	return false, err
}

func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
