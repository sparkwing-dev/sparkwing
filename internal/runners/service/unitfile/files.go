package unitfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Read reads a regular native unit file and reports whether its managed marker is present.
func Read(path, marker string) (body string, exists, managed bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, false, fmt.Errorf("native unit %s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, false, err
	}
	return string(data), true, strings.Contains(string(data), marker), nil
}

// Write publishes a private native unit atomically without changing an existing parent directory's mode.
func Write(path, body string) (result error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sparkwing-unit-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, err = f.WriteString(body)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
