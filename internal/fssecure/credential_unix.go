//go:build !windows

package fssecure

import (
	"errors"
	"os"
	"slices"
	"syscall"

	"golang.org/x/sys/unix"
)

func openCredentialFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
}

func verifyOwnerOnly(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("credential group is unavailable")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	return ownerOnly(info.Mode().Perm(), stat.Gid, append(groups, os.Getegid()))
}

// ownerOnly admits group read only for a group this process belongs to, which
// is how Kubernetes hands a non-root pod its secrets: mode 0440 under the pod's
// fsGroup.
func ownerOnly(perm os.FileMode, gid uint32, groups []int) error {
	if perm&0o037 != 0 || perm&0o040 != 0 && !slices.Contains(groups, int(gid)) {
		return errors.New("private key is readable by group or others; restrict it to its owner (mode 0400 or 0600), " +
			"or to group read under one of this process's own groups (mode 0440)")
	}
	return nil
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
