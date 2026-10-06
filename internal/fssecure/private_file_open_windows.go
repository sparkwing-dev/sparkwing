//go:build windows

package fssecure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateFileNativePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(abs, `\\?\`) {
		return abs, nil
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(abs, `\\`), nil
	}
	return `\\?\` + abs, nil
}

func openPrivateFile(path string, flag int) (*os.File, error) {
	if flag&os.O_CREATE == 0 {
		return openExistingPrivateFile(path, flag)
	}
	native, err := privateFileNativePath(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(native)
	if err != nil {
		return nil, err
	}
	acl, err := privateConfigACL(windows.NO_INHERITANCE)
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, err
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	descriptor, err = descriptor.ToSelfRelative()
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	// safety: withholding delete sharing pins the private creation while Go opens its flag-aware handle.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if flag&os.O_EXCL != 0 {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		return openExistingPrivateFile(path, flag&^(os.O_CREATE|os.O_EXCL))
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	created := os.NewFile(uintptr(handle), path)
	defer created.Close()
	expected, err := created.Stat()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, flag&^os.O_EXCL, FileMode)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("private file %q changed while it was created", path)
	}
	return file, nil
}

func openExistingPrivateFile(path string, flag int) (*os.File, error) {
	// safety: a replaced path must fail the identity check before truncation can touch its target.
	file, err := os.OpenFile(path, flag&^os.O_TRUNC, FileMode)
	if err != nil {
		return nil, err
	}
	if err := tightenOpen(file, FileMode); err != nil {
		_ = file.Close()
		return nil, err
	}
	if flag&os.O_TRUNC != 0 {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}
