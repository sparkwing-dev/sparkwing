// Package cachepressure exposes Sparkwing-owned pipeline-cache measurement and
// reclamation without exposing cache paths or entry layout.
package cachepressure

import (
	"context"
	"errors"
)

var errUnavailable = errors.New("pipeline cache pressure API unavailable")

// PruneOptions bounds one reclamation attempt.
type PruneOptions struct {
	ReclaimBytes int64
	MaxEntries   int
}

// PruneResult reports observed pressure and completed work.
type PruneResult struct {
	ObservedBytes  int64 `json:"observed_bytes"`
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
	Examined       int   `json:"examined_entries"`
	Reclaimed      int   `json:"reclaimed_entries"`
	Active         int   `json:"active_entries"`
	Busy           int   `json:"busy_entries"`
	GoalSatisfied  bool  `json:"goal_satisfied"`
	Exhausted      bool  `json:"exhausted"`
}

// Status reports managed and legacy pipeline-cache pressure.
type Status struct {
	ObservedBytes int64 `json:"observed_bytes"`
	EntryCount    int   `json:"entry_count"`
	ActiveEntries int   `json:"active_entries"`
	ActiveBytes   int64 `json:"active_bytes"`
	BusyEntries   int   `json:"busy_entries"`
	LegacyBytes   int64 `json:"legacy_bytes"`
	LegacyEntries int   `json:"legacy_entries"`
}

// Measure reports pipeline-cache pressure.
func Measure(context.Context) (Status, error) {
	return Status{}, errUnavailable
}

// Prune reclaims inactive entries within opts.
func Prune(context.Context, PruneOptions) (PruneResult, error) {
	return PruneResult{}, errUnavailable
}
