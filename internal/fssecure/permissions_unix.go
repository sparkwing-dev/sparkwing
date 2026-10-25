//go:build !windows

package fssecure

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func tighten(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

func tightenOpen(file *os.File, mode fs.FileMode) error { return file.Chmod(mode) }

func securePrivateDir(path string, expected os.FileInfo) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("open private directory %q", path)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.IsDir() || !os.SameFile(expected, opened) {
		return fmt.Errorf("private directory %q changed while it was inspected", path)
	}
	if err := file.Chmod(dirMode); err != nil {
		return err
	}
	secured, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(opened, secured) {
		return fmt.Errorf("private directory %q changed while it was secured", path)
	}
	return nil
}
