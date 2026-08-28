package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type summarizingNodeLog struct {
	inner       NodeLog
	persistNode func(md string)
	persistStep func(stepID, md string)
}

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
