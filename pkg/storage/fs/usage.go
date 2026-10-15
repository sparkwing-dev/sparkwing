// Package fs measures filesystem storage usage.
package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// UsageReporter measures physical files below Root.
type UsageReporter struct {
	Root string
}

// NewUsageReporter prepares a filesystem storage measurement root.
func NewUsageReporter(root string) (*UsageReporter, error) {
	if root == "" {
		return nil, errors.New("fs.NewUsageReporter: root required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &UsageReporter{Root: root}, nil
}

var _ storage.UsageReporter = (*UsageReporter)(nil)

// Usage totals regular files and marks an interrupted walk as partial.
func (s *UsageReporter) Usage(ctx context.Context) (storage.StoreUsage, error) {
	var usage storage.StoreUsage
	err := filepath.WalkDir(s.Root, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		usage.Objects++
		usage.Bytes += info.Size()
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			return storage.StoreUsage{}, fmt.Errorf("fs usage %s: %w", s.Root, err)
		}
		usage.Partial = true
	}
	usage.ObservedAt = time.Now().UTC()
	return usage, nil
}
