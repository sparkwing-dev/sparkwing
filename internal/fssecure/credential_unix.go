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

func verifyCredentialFile(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("credential target is writable by group or others; make it mode 0600, and on Kubernetes " +
			"set the pod's fsGroupChangePolicy to OnRootMismatch so a remount does not add group write again")
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
