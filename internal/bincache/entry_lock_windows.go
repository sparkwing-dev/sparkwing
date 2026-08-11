//go:build windows

package bincache

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type cacheLockMode uint8

const (
	cacheLockShared cacheLockMode = iota
	cacheLockExclusive
	cacheLockExclusiveNonblock
)

const cacheLockBytes = 1 << 30

func cacheLock(file *os.File, mode cacheLockMode) (bool, error) {
	var flags uint32
	if mode != cacheLockShared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if mode == cacheLockExclusiveNonblock {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, cacheLockBytes, 0, &overlapped)
	if err != nil {
		if mode == cacheLockExclusiveNonblock && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func cacheUnlock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, cacheLockBytes, 0, &overlapped)
}

func cacheLeaseReady(*os.File) error {
	return nil
}

func cacheRetainAcrossExec(*os.File) (func() error, error) {
	return func() error { return nil }, nil
}
