//go:build windows

package client

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func socketDirMissingAfterDial(sock string, dialErr error) bool {
	if !errors.Is(dialErr, windows.WSAENETDOWN) {
		return false
	}
	// hack: Windows AF_UNIX reports WSAENETDOWN when the socket's parent is absent.
	_, err := os.Stat(filepath.Dir(sock))
	return errors.Is(err, fs.ErrNotExist)
}
