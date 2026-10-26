//go:build !windows

package fssecure

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openCredentialFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
}

func verifyOwnerOnly(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("private key is readable by group or others; restrict it to its owner (mode 0400 or 0600)")
	}
	return nil
}

func verifyCredentialFile(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("credential target is writable by group or others")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("credential owner is unavailable")
	}
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return errors.New("credential owner is neither the current user nor root")
	}
	return nil
}
