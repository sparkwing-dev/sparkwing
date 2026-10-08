//go:build windows

package fssecure

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func mkdirPrivateTemp(parent, prefix string) (string, error) {
	descriptor, err := privateDirectorySecurityDescriptor()
	if err != nil {
		return "", err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	for range 10_000 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		path := filepath.Join(parent, prefix+hex.EncodeToString(random[:]))
		path16, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return "", err
		}
		if err := windows.CreateDirectory(path16, &attributes); err != nil {
			if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return "", &os.PathError{Op: "mkdirtemp", Path: path, Err: err}
		}
		if err := SecurePrivateDir(path); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("create unique private temporary directory under %s", parent)
}
