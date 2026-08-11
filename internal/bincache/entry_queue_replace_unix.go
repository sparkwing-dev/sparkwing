//go:build !windows

package bincache

import (
	"errors"
	"os"
)

func replaceCacheMetadata(source, destination string) error {
	return os.Rename(source, destination)
}

func syncCacheMetadataDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
