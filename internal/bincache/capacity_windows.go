//go:build windows

package bincache

import (
	"context"
)

func removeCacheEntryWithCapacity(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := removeCacheEntry(path); err != nil {
		return 0, err
	}
	return 0, nil
}
