//go:build !windows

package orchestrator

import (
	"os"
	"syscall"
)

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
