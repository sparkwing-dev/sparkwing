package fssecure

import (
	"errors"
	"fmt"
	"os"
)

// OpenPrivateConfig opens credential-bearing operator config only when the
// path is a stable, owner-only regular file.
func OpenPrivateConfig(path string) (*os.File, error) {
	return openPrivateConfig(path, openPrivateConfigFile)
}

// SecurePrivateDir gives credential-bearing temporary trees an owner-only
// boundary before any child files are created.
func SecurePrivateDir(path string) error { return securePrivateDir(path) }

func openPrivateConfig(path string, open func(string) (*os.File, error)) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("private config must be a regular file, not a symlink: %s", path)
	}
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("private config changed while it was opened")
	}
	if !opened.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("private config must be a regular file: %s", path)
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(current, opened) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("private config changed while it was opened")
	}
	if err := verifyOpenedPrivateConfig(path, f, opened); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("private config %s is not owner-only: %w", path, err)
	}
	return f, nil
}
