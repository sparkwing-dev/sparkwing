package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// stepStateNodeLog observes step_start / step_end / step_skipped
// records as they fan out and writes a corresponding node_steps row
// via the State backend. The bracketing log records still flow
// through to the JSONL file and pretty renderer; the wrapper only
// adds a side-channel write so the dashboard can read structured
// step state instead of re-parsing the log stream.
//
// Persist errors are intentionally swallowed: per-step state is
// dashboard ergonomics, never load-bearing for run correctness, and
// the canonical fact (the log record itself) still lands.
type stepStateNodeLog struct {
	inner   NodeLog
	persist func(event, stepID, outcome string)
}

// wrapNodeLogWithStepState returns inner unchanged when state is
// nil. The returned wrapper persists each step-lifecycle record to
// the State backend before delegating to inner.
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
				if outcome == "failed" {
					status = store.StepFailed
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
