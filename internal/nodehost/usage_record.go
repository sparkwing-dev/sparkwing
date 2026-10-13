package nodehost

import (
	"context"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// UsageStore is where an attempt's measured usage lands. The orchestrator's
// local state backend satisfies it, and the capacity record reads both
// writes back at run end.
type UsageStore interface {
	AddNodeUsage(ctx context.Context, runID, nodeID string, u store.NodeUsage) error
	AddNodeMetricSample(ctx context.Context, runID, nodeID string, sample store.MetricSample) error
}

// RecordUsage returns an [Attempts.Usage] hook that folds each attempt's
// session usage into its node's row. The host's measurement is the only
// source; SDK exec_end records never feed these numbers. A nil st returns a
// nil hook, for state that is not this machine's own runs store.
func RecordUsage(st UsageStore) func(context.Context, *NodeRun, Usage) {
	if st == nil {
		return nil
	}
	return func(ctx context.Context, n *NodeRun, u Usage) {
		if u.CPUTime <= 0 && u.MaxRSSBytes <= 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		runID, nodeID, logger := n.Run.ID, n.Plan.ID, n.Run.Logger
		// safety: the marker lands before the figures, so a reader never
		// prices leader-only usage as the whole session.
		if !u.SessionWide {
			markPartial(ctx, st, logger, runID, nodeID)
		}
		if err := st.AddNodeUsage(ctx, runID, nodeID, store.NodeUsage{
			CPUTime:     u.CPUTime,
			MaxRSSBytes: u.MaxRSSBytes,
			Wall:        u.Wall,
		}); err != nil {
			logger.Warn("nodehost: record node usage", "run", runID, "node", nodeID, "err", err)
		}
	}
}

func markPartial(ctx context.Context, st UsageStore, logger *slog.Logger, runID, nodeID string) {
	err := st.AddNodeMetricSample(ctx, runID, nodeID, store.MetricSample{Kind: store.MetricPartial, TS: time.Now()})
	if err == nil {
		return
	}
	// safety: a backend that refuses the partial kind would otherwise learn
	// from the reading unrestricted; every backend accepts the exclusion.
	if uerr := st.AddNodeMetricSample(ctx, runID, nodeID, store.MetricSample{Kind: store.MetricUnknown, TS: time.Now()}); uerr != nil {
		logger.Warn("nodehost: mark node usage partial", "run", runID, "node", nodeID, "err", err, "fallback_err", uerr)
	}
}
