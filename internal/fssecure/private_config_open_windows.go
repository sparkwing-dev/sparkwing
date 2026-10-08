//go:build windows

package fssecure

import (
	"os"

	"golang.org/x/sys/windows"
)

func openPrivateConfigFile(path string) (*os.File, error) {
	native, err := privateFileNativePath(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
