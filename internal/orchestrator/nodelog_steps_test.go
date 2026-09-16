package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type failingStepState struct {
	StateBackend
	err   error
	calls int
}

func (s *failingStepState) StartNodeStep(context.Context, string, string, string) error {
	s.calls++
	return s.err
}

func (s *failingStepState) FinishNodeStep(context.Context, string, string, string, string) error {
	s.calls++
	return s.err
}

func (s *failingStepState) SkipNodeStep(context.Context, string, string, string) error {
	s.calls++
	return s.err
}

type stepDiagnosticLog struct {
	records []sparkwing.LogRecord
	closed  bool
}

func (*stepDiagnosticLog) Log(string, string) {}

func (l *stepDiagnosticLog) Emit(rec sparkwing.LogRecord) {
	l.records = append(l.records, rec)
}

func (l *stepDiagnosticLog) Close() error {
	l.closed = true
	return nil
}

func TestStepStateFailuresRemainVisibleWithoutReplacingWorkOutcome(t *testing.T) {
	for _, event := range []string{sparkwing.EventStepStart, sparkwing.EventStepEnd, sparkwing.EventStepSkipped} {
		t.Run(event, func(t *testing.T) {
			const secret = "private-backend-token"
			masker := secrets.NewMasker()
			masker.Register(secret)
			ctx, cancel := context.WithCancel(secrets.WithMasker(context.Background(), masker))
			cancel()
			state := &failingStepState{err: errors.New(secret + strings.Repeat(" backend unavailable", 400))}
			inner := &stepDiagnosticLog{}
			log := wrapNodeLogWithStepState(ctx, inner, state, "run", "node")
			log.Emit(sparkwing.LogRecord{Event: event, Msg: "step", Attrs: map[string]any{"outcome": string(sparkwing.Cancelled)}})
			if state.calls != 1 || len(inner.records) != 2 {
				t.Fatalf("writes=%d records=%d, want one write, one diagnostic and the original event", state.calls, len(inner.records))
			}
			diagnostic, original := inner.records[0], inner.records[1]
			if diagnostic.Event != "state_write_failed" || diagnostic.Level != "warn" || diagnostic.JobID != "node" || diagnostic.Step != "step" || diagnostic.Attrs["run_id"] != "run" || diagnostic.Attrs["operation"] != event {
				t.Fatalf("missing failure identity: %+v", diagnostic)
			}
			message, ok := diagnostic.Attrs["error"].(string)
			if !ok || !strings.Contains(message, "***") || strings.Contains(message, secret) || len(message) > 4200 || !strings.Contains(message, "truncated") {
				t.Fatalf("error must be masked and bounded (length %d)", len(message))
			}
			if original.Event != event || original.Msg != "step" || original.Attrs["outcome"] != string(sparkwing.Cancelled) {
				t.Fatalf("work event changed: %+v", original)
			}
			if err := nodeLogFatal(log); err != nil {
				t.Fatalf("projection failure replaced execution outcome: %v", err)
			}
			if err := log.Close(); err != nil || !inner.closed {
				t.Fatalf("log cleanup did not complete: %v", err)
			}
		})
	}
}

func TestSuccessfulStepStateWriteEmitsOnlyWorkEvent(t *testing.T) {
	state := &failingStepState{}
	inner := &stepDiagnosticLog{}
	log := wrapNodeLogWithStepState(context.Background(), inner, state, "run", "node")
	log.Emit(sparkwing.LogRecord{Event: sparkwing.EventStepStart, Msg: "step"})
	if state.calls != 1 || len(inner.records) != 1 || inner.records[0].Event != sparkwing.EventStepStart {
		t.Fatalf("successful write emitted a diagnostic: writes=%d records=%+v", state.calls, inner.records)
	}
}
