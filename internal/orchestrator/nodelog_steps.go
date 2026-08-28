package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepStateNodeLog struct {
	inner   NodeLog
	persist func(event, stepID, outcome string)
}

func wrapNodeLogWithStepState(inner NodeLog, state StateBackend, runID, nodeID string) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	ctx := context.Background()
	return &stepStateNodeLog{
		inner: inner,
		persist: func(event, stepID, outcome string) {
			if stepID == "" {
				return
			}
			switch event {
			case sparkwing.EventStepStart:
				_ = state.StartNodeStep(ctx, runID, nodeID, stepID)
			case sparkwing.EventStepEnd:
				status := store.StepPassed
				switch outcome {
				case string(sparkwing.Failed):
					status = store.StepFailed
				case string(sparkwing.Cancelled):
					status = store.StepCancelled
				}
				_ = state.FinishNodeStep(ctx, runID, nodeID, stepID, status)
			case sparkwing.EventStepSkipped:
				_ = state.SkipNodeStep(ctx, runID, nodeID, stepID)
			}
		},
	}
}

func (l *stepStateNodeLog) Log(level, msg string) { l.inner.Log(level, msg) }

func (l *stepStateNodeLog) Emit(rec sparkwing.LogRecord) {
	switch rec.Event {
	case sparkwing.EventStepStart, sparkwing.EventStepEnd, sparkwing.EventStepSkipped:
		outcome, _ := rec.Attrs["outcome"].(string)
		l.persist(rec.Event, rec.Msg, outcome)
	}
	l.inner.Emit(rec)
}

func (l *stepStateNodeLog) Close() error { return l.inner.Close() }

func (l *stepStateNodeLog) Fatal() error {
	if f, ok := l.inner.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

func (l *stepStateNodeLog) Drops() (int, string) {
	if d, ok := l.inner.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}
