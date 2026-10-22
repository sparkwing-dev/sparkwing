//go:build !windows

package bincache

import "os"

func replaceCacheMetadata(source, destination string) error {
	return os.Rename(source, destination)
}
