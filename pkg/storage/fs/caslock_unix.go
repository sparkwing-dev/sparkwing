//go:build !windows

package fs

import (
	"errors"
	"os"
	"syscall"
)

func tryLockExclusive(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return false, nil
	case errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EOPNOTSUPP),
		errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOLCK):
		return false, errLockUnsupported
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
