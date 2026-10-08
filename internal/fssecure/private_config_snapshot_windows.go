//go:build windows

package fssecure

import (
	"errors"
	"os"
)

func privateConfigSnapshot(path string) (os.FileInfo, error) {
	// bug: Windows path stats resolve file identity lazily, after the path may have been replaced.
	file, err := openPrivateConfigFile(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	return info, errors.Join(err, file.Close())
}
