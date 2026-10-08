//go:build windows

package store

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openOutputForSync(path string) (*os.File, error) {
	// bug: FlushFileBuffers requires a writable handle on Windows.
	return os.OpenFile(path, os.O_RDWR, 0)
}

func syncDir(string) error {
	// bug: Windows cannot flush directory handles; file sync and write-through publication happen before this point.
	return nil
}

func renameOutputFile(old, new string) error {
	toNative := func(path string) (*uint16, error) {
		full, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(full, `\\?\`) {
			if strings.HasPrefix(full, `\\`) {
				full = `\\?\UNC\` + full[2:]
			} else {
				full = `\\?\` + full
			}
		}
		return windows.UTF16PtrFromString(full)
	}
	source, err := toNative(old)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
	}
	destination, err := toNative(new)
	if err == nil {
		err = windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
	}
	return nil
}
