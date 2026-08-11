// Package cachepressure exposes Sparkwing-owned pipeline-cache measurement and
// reclamation without exposing cache paths or entry layout.
package cachepressure

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

// PruneOptions bounds one reclamation attempt.
type PruneOptions struct {
	ReclaimBytes int64
	MaxEntries   int
}

// PruneResult reports observed pressure and completed work.
type PruneResult = bincache.PruneResult

// Status reports managed and legacy pipeline-cache pressure.
type Status = bincache.CacheStatus

// Measure reports pipeline-cache pressure.
func Measure(ctx context.Context) (Status, error) {
	return bincache.Status(ctx, "")
}

// Prune reclaims inactive entries within opts.
func Prune(ctx context.Context, opts PruneOptions) (PruneResult, error) {
	return bincache.Prune(ctx, bincache.PruneOptions{
		ReclaimBytes: opts.ReclaimBytes,
		MaxEntries:   opts.MaxEntries,
	})
}
