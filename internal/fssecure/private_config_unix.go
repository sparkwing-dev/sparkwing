//go:build !windows

package fssecure

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func VerifyPrivateConfig(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("mode must be 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file owner is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("file owner uid %d is not current uid %d", stat.Uid, os.Geteuid())
	}
	return nil
}

func verifyOpenedPrivateConfig(path string, _ *os.File, info os.FileInfo) error {
	return VerifyPrivateConfig(path, info)
}

// SecurePrivateConfig restricts a config to its owner on POSIX filesystems.
func SecurePrivateConfig(path string) error { return os.Chmod(path, FileMode) }
