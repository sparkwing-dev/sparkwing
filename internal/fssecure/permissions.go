package fssecure

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

func EnsureDir(path string) error {
	target := dirMode
	if info, err := os.Lstat(path); err == nil {
		// safety: chmod follows links, so a symlinked root would move the tighten off-path.
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse to prepare private directory through symlink %q", path)
		}
		target = info.Mode().Perm()&dirMode | fsGroupMode(info)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, dirMode); err != nil {
		return err
	}
	if err := tighten(path, target); err != nil {
		return fmt.Errorf("set mode %04o on %s: %w", target.Perm(), path, err)
	}
	return nil
}

// SecurePrivateDir restricts an existing directory to the current user and
// rejects links or replacements while applying the platform policy.
func SecurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse to secure private directory through symlink %q", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("private path %q is not a directory", path)
	}
	if err := securePrivateDir(path, info); err != nil {
		return fmt.Errorf("secure private directory %s: %w", path, err)
	}
	return nil
}

func OpenFile(path string, flag int) (*os.File, error) {
	f, err := openPrivateFile(path, flag)
	if err != nil {
		return nil, err
	}
	if err := tightenOpen(f, fileMode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// TightenOpen applies the private file mode to a file the caller has
// already opened, such as through an [os.Root].
func TightenOpen(f *os.File) error { return tightenOpen(f, fileMode) }

func WriteFile(path string, data []byte) error {
	f, err := OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
