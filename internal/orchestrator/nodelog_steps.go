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

func wrapNodeLogWithStepState(ctx context.Context, inner NodeLog, state StateBackend, runID, nodeID string) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	ctx = context.WithoutCancel(ctx)
	return &stepStateNodeLog{
		inner: inner,
		persist: func(event, stepID, outcome string) {
			if stepID == "" {
				return
			}
			var err error
			switch event {
			case sparkwing.EventStepStart:
				err = state.StartNodeStep(ctx, runID, nodeID, stepID)
			case sparkwing.EventStepEnd:
				status := store.StepPassed
				switch outcome {
				case string(sparkwing.Failed):
					status = store.StepFailed
				case string(sparkwing.Cancelled):
					status = store.StepCancelled
				}
				err = state.FinishNodeStep(ctx, runID, nodeID, stepID, status)
			case sparkwing.EventStepSkipped:
				err = state.SkipNodeStep(ctx, runID, nodeID, stepID)
			}
			if err != nil {
				// safety: recording failure must not replace the outcome of work that already happened.
				inner.Emit(sparkwing.LogRecord{
					Level: "warn", Event: "state_write_failed", JobID: nodeID, Step: stepID,
					Msg: "step state write failed",
					Attrs: map[string]any{
						"run_id": runID, "operation": event,
						"error": boundedFailureText(ctx, runID, nodeID, err),
					},
				})
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

func (l *stepStateNodeLog) BindExecutionAttempt(ordinal int) error {
	return bindNodeLogExecutionAttempt(l.inner, ordinal)
}

func (l *stepStateNodeLog) FlushExecutionAttempt() error {
	return flushNodeLogExecutionAttempt(l.inner)
}

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
