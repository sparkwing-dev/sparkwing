// Package cachecontrol exposes Sparkwing-owned pipeline-cache sharing, measurement
// and reclamation without exposing cache paths or entry layout.
package cachecontrol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// ShareWithHome connects a private Sparkwing home's pipeline cache to this
// process's existing cache. Configuration, credentials and stores stay private.
// The destination must be unused or already linked to the same cache.
func ShareWithHome(home string) error {
	source, err := filepath.Abs(filepath.Join(bincache.SparkwingHome(), "cache", "pipelines"))
	if err != nil {
		return err
	}
	dest := filepath.Join(home, "cache", "pipelines")
	if err := os.MkdirAll(source, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.Symlink(source, dest); err != nil {
		if target, readErr := os.Readlink(dest); readErr == nil && target == source {
			return nil
		}
		return fmt.Errorf("share pipeline cache: %w", err)
	}
	return nil
}

// PruneOptions bounds one reclamation attempt.
type PruneOptions struct {
	RemoveBytes int64
	MaxEntries  int
}

// PruneResult reports observed cache state and completed work.
type PruneResult = bincache.PruneResult

// Status reports managed pipeline-cache state.
type Status = bincache.CacheStatus

// Measure reports pipeline-cache state.
func Measure(ctx context.Context) (Status, error) {
	return bincache.Status(ctx, "")
}

// Prune removes inactive entries toward a logical-byte goal within opts.
// Logical bytes are file lengths, not physical disk space. Callers must remeasure
// filesystem capacity before admission.
func Prune(ctx context.Context, opts PruneOptions) (PruneResult, error) {
	return bincache.Prune(ctx, bincache.PruneOptions{
		RemoveBytes: opts.RemoveBytes,
		MaxEntries:  opts.MaxEntries,
	})
}
