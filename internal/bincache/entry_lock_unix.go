//go:build !windows

package bincache

import (
	"errors"
	"os"
	"syscall"
)

type cacheLockMode uint8

const (
	cacheLockShared cacheLockMode = iota
	cacheLockExclusive
	cacheLockExclusiveNonblock
)

func cacheLock(file *os.File, mode cacheLockMode) (bool, error) {
	flag := syscall.LOCK_SH
	if mode != cacheLockShared {
		flag = syscall.LOCK_EX
	}
	if mode == cacheLockExclusiveNonblock {
		flag |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), flag); err != nil {
		if mode == cacheLockExclusiveNonblock && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func cacheUnlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
