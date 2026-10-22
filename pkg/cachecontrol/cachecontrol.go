// Package cachecontrol exposes Sparkwing-owned pipeline-cache measurement and
// reclamation without exposing cache paths or entry layout.
package cachecontrol

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

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
