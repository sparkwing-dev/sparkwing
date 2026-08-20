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
	cacheLockSharedNonblock
	cacheLockExclusive
	cacheLockExclusiveNonblock
)

func cacheLock(file *os.File, mode cacheLockMode) (bool, error) {
	flag := syscall.LOCK_SH
	if mode != cacheLockShared && mode != cacheLockSharedNonblock {
		flag = syscall.LOCK_EX
	}
	if mode == cacheLockSharedNonblock || mode == cacheLockExclusiveNonblock {
		flag |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), flag); err != nil {
		if (mode == cacheLockSharedNonblock || mode == cacheLockExclusiveNonblock) && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func cacheUnlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func cacheLeaseReady(file *os.File) error {
	_, err := cacheLock(file, cacheLockShared)
	return err
}
