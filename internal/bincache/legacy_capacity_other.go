//go:build !darwin && !linux && !windows

package bincache

import (
	"context"
)

func removeLegacyCacheEntry(_ context.Context, path string) (int64, error) {
	if err := removeCacheEntry(path); err != nil {
		return 0, err
	}
	return 0, nil
}
