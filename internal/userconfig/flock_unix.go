//go:build !windows

package userconfig

import (
	"errors"
	"os"
	"syscall"
)

func flockWait(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func flockUnlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
