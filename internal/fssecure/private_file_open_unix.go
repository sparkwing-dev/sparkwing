//go:build !windows

package fssecure

import "os"

func openPrivateFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, FileMode)
}
