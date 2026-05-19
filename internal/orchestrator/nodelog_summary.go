package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// summarizingNodeLog observes LogRecords for the node_summary event
// emitted by sparkwing.Summary and forwards the markdown payload to
// the State backend. Summaries fired inside a step body (rec.Step
// set) land on the per-step row; everything else lands on the node
// row. Persistence is overwrite-on-write: the last value per
// (node, step) scope wins.
//
// The wrapper holds its own background context for the persist call
// so a summary that fires late in a canceled step still lands.
// Persist errors are intentionally swallowed: summaries are advisory
// metadata, never load-bearing for run correctness.
type summarizingNodeLog struct {
	inner       NodeLog
	persistNode func(md string)
	persistStep func(stepID, md string)
}

// wrapNodeLogWithSummary returns inner unchanged when state is nil.
// The returned wrapper writes summary markdown to state and then
// delegates the original record to inner so the JSONL log file,
// pretty renderer, and dashboard tail all still see the event.
//
// Summary routing: when the LogRecord carries a Step (set by
// recordEnvelope while inside a step body), the markdown lands on
// the node_steps row instead of the node row. Summaries fired
// between steps (node setup, after-hooks, etc.) land on the node
// row. The two rows are disjoint -- a step body's summary belongs on
// that step, never both places.
func wrapNodeLogWithSummary(inner NodeLog, state StateBackend, runID, nodeID string) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	ctx := context.Background()
	return &summarizingNodeLog{
		inner: inner,
		persistNode: func(md string) {
			_ = state.SetNodeSummary(ctx, runID, nodeID, md)
		},
		persistStep: func(stepID, md string) {
			_ = state.SetStepSummary(ctx, runID, nodeID, stepID, md)
		},
	}
}

func (l *summarizingNodeLog) Log(level, msg string) { l.inner.Log(level, msg) }

func (l *summarizingNodeLog) Emit(rec sparkwing.LogRecord) {
	if rec.Event == sparkwing.EventNodeSummary {
		md := rec.Msg
		if md == "" {
			if m, ok := rec.Attrs["markdown"].(string); ok {
				md = m
			}
		}
		if rec.Step != "" {
			l.persistStep(rec.Step, md)
		} else {
			l.persistNode(md)
		}
	}
	l.inner.Emit(rec)
}

func (l *summarizingNodeLog) Close() error { return l.inner.Close() }

func (l *summarizingNodeLog) Fatal() error {
	if f, ok := l.inner.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

func (l *summarizingNodeLog) Drops() (int, string) {
	if d, ok := l.inner.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}
