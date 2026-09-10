//go:build !windows

package main

import (
	"os"
	"syscall"
)

func dashboardTryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK {
		return false, nil
	}
	return false, err
}

func dashboardLock(f *os.File) error {
	held, err := dashboardTryLock(f)
	if err != nil {
		return err
	}
	if !held {
		return os.ErrExist
	}
	return nil
}
