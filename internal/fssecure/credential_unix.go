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
	gid, groups, err := fileGroup(info)
	if err != nil {
		return err
	}
	return ownerOnly(info.Mode().Perm(), gid, groups)
}

// fileGroup returns the file's group and the groups this process belongs to.
func fileGroup(info os.FileInfo) (uint32, []int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, nil, errors.New("credential group is unavailable")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return 0, nil, err
	}
	return stat.Gid, append(groups, os.Getegid()), nil
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
	gid, groups, err := fileGroup(info)
	if err != nil {
		return err
	}
	if err := groupWritableOnlyByOwnGroup(info.Mode().Perm(), gid, groups); err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return errors.New("credential owner is neither the current user nor root")
	}
	return nil
}

// groupWritableOnlyByOwnGroup admits group write only for a group this process
// belongs to: kubelet makes every file on a pod volume group-writable under the
// pod's fsGroup.
func groupWritableOnlyByOwnGroup(perm os.FileMode, gid uint32, groups []int) error {
	if perm&0o002 != 0 || perm&0o020 != 0 && !slices.Contains(groups, int(gid)) {
		return errors.New("credential target is writable by others or by a group this process is not in; " +
			"make it mode 0600, or group-writable only under one of this process's own groups (the pod fsGroup)")
	}
	return nil
}
