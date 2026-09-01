package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type annotatingNodeLog struct {
	inner       NodeLog
	persistNode func(msg string)
	persistStep func(stepID, msg string)
}

func wrapNodeLogWithAnnotations(inner NodeLog, state StateBackend, runID, nodeID string) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	ctx := context.Background()
	return &annotatingNodeLog{
		inner: inner,
		persistNode: func(msg string) {
			_ = state.AppendNodeAnnotation(ctx, runID, nodeID, msg)
		},
		persistStep: func(stepID, msg string) {
			_ = state.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg)
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
			if rec.Step != "" {
				l.persistStep(rec.Step, msg)
			} else {
				l.persistNode(msg)
			}
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
