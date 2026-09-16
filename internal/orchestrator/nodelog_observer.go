package orchestrator

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type observingNodeLog struct {
	NodeLog
	ctx           context.Context
	state         StateBackend
	runID, nodeID string
}

func wrapNodeLogWithStateObservations(
	ctx context.Context, inner NodeLog, state StateBackend, runID, nodeID string,
) NodeLog {
	if inner == nil || state == nil {
		return inner
	}
	return &observingNodeLog{
		NodeLog: inner, ctx: context.WithoutCancel(ctx), state: state,
		runID: runID, nodeID: nodeID,
	}
}

func (l *observingNodeLog) Emit(rec sparkwing.LogRecord) {
	message, stepID, err := l.persist(rec)
	if err != nil {
		// safety: observational state must not determine the work's execution outcome.
		l.NodeLog.Emit(sparkwing.LogRecord{
			Level: "warn", Event: "state_write_failed", JobID: l.nodeID, Step: stepID,
			Msg: message,
			Attrs: map[string]any{
				"run_id": l.runID, "operation": rec.Event,
				"error": boundedFailureText(l.ctx, l.runID, l.nodeID, err),
			},
		})
	}
	l.NodeLog.Emit(rec)
}

func (l *observingNodeLog) persist(rec sparkwing.LogRecord) (message, stepID string, err error) {
	switch rec.Event {
	case sparkwing.EventStepStart:
		if rec.Msg == "" {
			return "", "", nil
		}
		return "step state write failed", rec.Msg,
			l.state.StartNodeStep(l.ctx, l.runID, l.nodeID, rec.Msg)
	case sparkwing.EventStepEnd:
		if rec.Msg == "" {
			return "", "", nil
		}
		status := store.StepPassed
		if outcome, _ := rec.Attrs["outcome"].(string); outcome == string(sparkwing.Failed) {
			status = store.StepFailed
		} else if outcome == string(sparkwing.Cancelled) {
			status = store.StepCancelled
		}
		return "step state write failed", rec.Msg,
			l.state.FinishNodeStep(l.ctx, l.runID, l.nodeID, rec.Msg, status)
	case sparkwing.EventStepSkipped:
		if rec.Msg == "" {
			return "", "", nil
		}
		return "step state write failed", rec.Msg,
			l.state.SkipNodeStep(l.ctx, l.runID, l.nodeID, rec.Msg)
	case sparkwing.EventNodeAnnotation:
		message := rec.Msg
		if message == "" {
			message, _ = rec.Attrs["message"].(string)
		}
		if message == "" {
			return "", "", nil
		}
		if rec.Step != "" {
			return "annotation state write failed", rec.Step,
				l.state.AppendStepAnnotation(l.ctx, l.runID, l.nodeID, rec.Step, message)
		}
		return "annotation state write failed", "",
			l.state.AppendNodeAnnotation(l.ctx, l.runID, l.nodeID, message)
	case sparkwing.EventNodeSummary:
		markdown := rec.Msg
		if markdown == "" {
			markdown, _ = rec.Attrs["markdown"].(string)
		}
		if rec.Step != "" {
			return "summary state write failed", rec.Step,
				l.state.SetStepSummary(l.ctx, l.runID, l.nodeID, rec.Step, markdown)
		}
		return "summary state write failed", "",
			l.state.SetNodeSummary(l.ctx, l.runID, l.nodeID, markdown)
	default:
		return "", "", nil
	}
}

func (l *observingNodeLog) BindExecutionAttempt(ordinal int) error {
	return bindNodeLogExecutionAttempt(l.NodeLog, ordinal)
}

func (l *observingNodeLog) FlushExecutionAttempt() error {
	return flushNodeLogExecutionAttempt(l.NodeLog)
}

func (l *observingNodeLog) Fatal() error {
	if fatal, ok := l.NodeLog.(interface{ Fatal() error }); ok {
		return fatal.Fatal()
	}
	return nil
}

func (l *observingNodeLog) Drops() (int, string) {
	if drops, ok := l.NodeLog.(interface{ Drops() (int, string) }); ok {
		return drops.Drops()
	}
	return 0, ""
}
