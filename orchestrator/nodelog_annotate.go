package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// annotatingNodeLog observes LogRecords for the node_annotation event
// emitted by sparkwing.Annotate and forwards each one to the State
// backend so it lands on the persistent node row. Non-annotation
// records pass through unchanged.
//
// The wrapper holds its own background context for the persist call
// so an annotation that fires late in a canceled step still lands.
// Persist errors are intentionally swallowed: annotations are
// advisory metadata, never load-bearing for run correctness.
type annotatingNodeLog struct {
	inner   NodeLog
	persist func(msg string)
}

// wrapNodeLogWithAnnotations returns inner unchanged when state is
// nil. The returned wrapper writes annotation messages to state and
// then delegates the original record to inner so the JSONL log file,
// pretty renderer, and dashboard tail all still see the event.
func wrapNodeLogWithAnnotations(inner NodeLog, state StateBackend, runID, nodeID string) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	ctx := context.Background()
	return &annotatingNodeLog{
		inner: inner,
		persist: func(msg string) {
			_ = state.AppendNodeAnnotation(ctx, runID, nodeID, msg)
		},
	}
}

func (l *annotatingNodeLog) Log(level, msg string) { l.inner.Log(level, msg) }

func (l *annotatingNodeLog) Emit(rec sparkwing.LogRecord) {
	if rec.Event == sparkwing.EventNodeAnnotation {
		msg := rec.Msg
		if msg == "" {
			if m, ok := rec.Attrs["message"].(string); ok {
				msg = m
			}
		}
		if msg != "" {
			l.persist(msg)
		}
	}
	l.inner.Emit(rec)
}

func (l *annotatingNodeLog) Close() error { return l.inner.Close() }

func (l *annotatingNodeLog) Fatal() error {
	if f, ok := l.inner.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

func (l *annotatingNodeLog) Drops() (int, string) {
	if d, ok := l.inner.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}
