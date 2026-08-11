//go:build !windows

package bincache

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
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

func cacheLeaseReady(file *os.File) error {
	_, err := cacheLock(file, cacheLockShared)
	return err
}

func cacheRetainAcrossExec(file *os.File) (func() error, error) {
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		return nil, err
	}
	return func() error {
		_, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, flags)
		return err
	}, nil
}
