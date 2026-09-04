//go:build !windows

package fssecure

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPrivateConfigFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
