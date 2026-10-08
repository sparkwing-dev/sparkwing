//go:build windows

package bincache

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func legacyRetirementBusy(ctx context.Context, path string, renameErr error) (bool, error) {
	if errors.Is(renameErr, windows.ERROR_SHARING_VIOLATION) {
		return true, nil
	}
	if !errors.Is(renameErr, windows.ERROR_ACCESS_DENIED) {
		return false, nil
	}
	busy := false
	err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		wide, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		// safety: a delete-sharing violation proves a live handle; reparse points must not be followed.
		handle, err := windows.CreateFile(wide, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			busy = true
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		return windows.CloseHandle(handle)
	})
	return busy, err
}
