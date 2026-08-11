//go:build darwin || linux

package bincache

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"path/filepath"
	"syscall"
)

var legacyFilesystemAvailableBytes = filesystemAvailableBytes

func removeCacheEntryWithCapacity(ctx context.Context, path string) (int64, error) {
	allocated, err := cacheAllocatedBytes(ctx, path)
	if err != nil {
		return 0, err
	}
	before, err := legacyFilesystemAvailableBytes(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	if err := removeCacheEntry(path); err != nil {
		return 0, err
	}
	after, err := legacyFilesystemAvailableBytes(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	delta := after - before
	if delta < 0 {
		delta = 0
	}
	if delta > allocated {
		delta = allocated
	}
	return delta, nil
}

func cacheAllocatedBytes(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("legacy cache allocated size is unavailable")
		}
		bytes := stat.Blocks * 512
		if bytes > 0 && total > math.MaxInt64-bytes {
			return errors.New("legacy cache allocated size overflow")
		}
		total += bytes
		return nil
	})
	return total, err
}

func filesystemAvailableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize != 0 && blocks > math.MaxInt64/blockSize {
		return math.MaxInt64, nil
	}
	return int64(blocks * blockSize), nil
}
