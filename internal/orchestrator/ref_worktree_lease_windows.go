//go:build windows

package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func openRefWorktreeLease(path string) (*os.File, error) {
	initial, err := fssecure.OpenFile(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer initial.Close()
	before, err := initial.Stat()
	if err != nil {
		return nil, err
	}
	native, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(native, `\\?\`) {
		if strings.HasPrefix(native, `\\`) {
			native = `\\?\UNC\` + strings.TrimPrefix(native, `\\`)
		} else {
			native = `\\?\` + native
		}
	}
	name, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return nil, err
	}
	// bug: sweeping must unlink a lease while its byte lock still excludes contenders.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("ref worktree lease changed while opening %s", path)
	}
	return file, nil
}
