package orchestrator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type observationCall struct {
	operation, step, value, status string
}

type observationState struct {
	StateBackend
	err           error
	calls         []observationCall
	runID, nodeID string
	ctxErr        error
}

func (s *observationState) record(ctx context.Context, runID, nodeID string, call observationCall) error {
	s.runID, s.nodeID, s.ctxErr = runID, nodeID, ctx.Err()
	s.calls = append(s.calls, call)
	return s.err
}

func (s *observationState) StartNodeStep(ctx context.Context, runID, nodeID, step string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "start", step: step})
}

func (s *observationState) FinishNodeStep(ctx context.Context, runID, nodeID, step, status string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "finish", step: step, status: status})
}

func (s *observationState) SkipNodeStep(ctx context.Context, runID, nodeID, step string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "skip", step: step})
}

func (s *observationState) AppendNodeAnnotation(ctx context.Context, runID, nodeID, value string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "annotate-node", value: value})
}

func (s *observationState) AppendStepAnnotation(ctx context.Context, runID, nodeID, step, value string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "annotate-step", step: step, value: value})
}

func (s *observationState) SetNodeSummary(ctx context.Context, runID, nodeID, value string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "summarize-node", value: value})
}

func (s *observationState) SetStepSummary(ctx context.Context, runID, nodeID, step, value string) error {
	return s.record(ctx, runID, nodeID, observationCall{operation: "summarize-step", step: step, value: value})
}

type observationLog struct {
	records              []sparkwing.LogRecord
	lines                []string
	closed, bound, flush int
	closeErr, bindErr    error
	flushErr, fatalErr   error
	drops                int
	dropReason           string
}

func (l *observationLog) Log(level, msg string) { l.lines = append(l.lines, level+":"+msg) }
func (l *observationLog) Emit(rec sparkwing.LogRecord) {
	l.records = append(l.records, rec)
}
func (l *observationLog) Close() error { l.closed++; return l.closeErr }
func (l *observationLog) BindExecutionAttempt(ordinal int) error {
	l.bound = ordinal
	return l.bindErr
}
func (l *observationLog) FlushExecutionAttempt() error { l.flush++; return l.flushErr }
func (l *observationLog) Fatal() error                 { return l.fatalErr }
func (l *observationLog) Drops() (int, string)         { return l.drops, l.dropReason }

func TestNodeLogObserverPersistsEachRecordFamily(t *testing.T) {
	state := &observationState{}
	inner := &observationLog{}
	log := wrapNodeLogWithStateObservations(t.Context(), inner, state, "run", "node")
	records := []sparkwing.LogRecord{
		{Event: sparkwing.EventStepStart, Msg: "compile"},
		{Event: sparkwing.EventStepEnd, Msg: "compile", Attrs: map[string]any{"outcome": string(sparkwing.Failed)}},
		{Event: sparkwing.EventStepEnd, Msg: "compile", Attrs: map[string]any{"outcome": string(sparkwing.Cancelled)}},
		{Event: sparkwing.EventStepSkipped, Msg: "compile"},
		{Event: sparkwing.EventNodeAnnotation, Msg: "deployed"},
		{Event: sparkwing.EventNodeAnnotation, Step: "publish", Attrs: map[string]any{"message": "ready"}},
		{Event: sparkwing.EventNodeAnnotation},
		{Event: sparkwing.EventNodeSummary, Msg: "# result"},
		{Event: sparkwing.EventNodeSummary, Step: "publish", Attrs: map[string]any{"markdown": "# ready"}},
		{Event: sparkwing.EventNodeSummary},
	}
	for _, rec := range records {
		log.Emit(rec)
	}
	want := []observationCall{
		{operation: "start", step: "compile"},
		{operation: "finish", step: "compile", status: store.StepFailed},
		{operation: "finish", step: "compile", status: store.StepCancelled},
		{operation: "skip", step: "compile"},
		{operation: "annotate-node", value: "deployed"},
		{operation: "annotate-step", step: "publish", value: "ready"},
		{operation: "summarize-node", value: "# result"},
		{operation: "summarize-step", step: "publish", value: "# ready"},
		{operation: "summarize-node"},
	}
	if !reflect.DeepEqual(inner.records, records) {
		t.Fatalf("forwarded records = %+v, want originals %+v", inner.records, records)
	}
	if !reflect.DeepEqual(state.calls, want) {
		t.Fatalf("state calls = %+v, want %+v", state.calls, want)
	}
	if state.runID != "run" || state.nodeID != "node" || state.ctxErr != nil {
		t.Fatalf("state identity = %q/%q, context error %v", state.runID, state.nodeID, state.ctxErr)
	}
}

func TestNodeLogObserverReportsBoundedFailuresBeforeTheOriginalRecord(t *testing.T) {
	const secret = "private-backend-token"
	tests := []struct {
		name, message, step string
		rec                 sparkwing.LogRecord
	}{
		{"step", "step state write failed", "compile", sparkwing.LogRecord{Event: sparkwing.EventStepStart, Msg: "compile"}},
		{"annotation", "annotation state write failed", "", sparkwing.LogRecord{Event: sparkwing.EventNodeAnnotation, Msg: "note"}},
		{"summary", "summary state write failed", "publish", sparkwing.LogRecord{Event: sparkwing.EventNodeSummary, Step: "publish", Msg: "# result"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			masker := secrets.NewMasker()
			masker.Register(secret)
			ctx, cancel := context.WithCancel(secrets.WithMasker(context.Background(), masker))
			cancel()
			state := &observationState{err: errors.New(secret + strings.Repeat(" backend unavailable", 400))}
			inner := &observationLog{}
			log := wrapNodeLogWithStateObservations(ctx, inner, state, "run", "node")
			log.Emit(tc.rec)
			if len(state.calls) != 1 || len(inner.records) != 2 {
				t.Fatalf("writes=%d records=%d, want one write, one diagnostic and the original event", len(state.calls), len(inner.records))
			}
			if state.ctxErr != nil {
				t.Fatalf("observation inherited canceled context: %v", state.ctxErr)
			}
			diagnostic, original := inner.records[0], inner.records[1]
			if diagnostic.Event != "state_write_failed" || diagnostic.Level != "warn" ||
				diagnostic.JobID != "node" || diagnostic.Step != tc.step || diagnostic.Msg != tc.message ||
				diagnostic.Attrs["run_id"] != "run" || diagnostic.Attrs["operation"] != tc.rec.Event {
				t.Fatalf("failure identity = %+v", diagnostic)
			}
			message, ok := diagnostic.Attrs["error"].(string)
			if !ok || !strings.Contains(message, "***") || strings.Contains(message, secret) ||
				len(message) > 4200 || !strings.Contains(message, "truncated") {
				t.Fatalf("error must be masked and bounded (length %d)", len(message))
			}
			if !reflect.DeepEqual(original, tc.rec) {
				t.Fatalf("original record changed: %+v", original)
			}
			if err := nodeLogFatal(log); err != nil {
				t.Fatalf("observation failure replaced execution outcome: %v", err)
			}
		})
	}
}

func TestNodeLogObserverForwardsTheUnderlyingLogLifecycle(t *testing.T) {
	closeErr := errors.New("close")
	bindErr := errors.New("bind")
	flushErr := errors.New("flush")
	fatalErr := errors.New("fatal")
	inner := &observationLog{
		closeErr: closeErr, bindErr: bindErr, flushErr: flushErr, fatalErr: fatalErr,
		drops: 3, dropReason: "backend unavailable",
	}
	log := wrapNodeLogWithStateObservations(t.Context(), inner, &observationState{}, "run", "node")
	log.Log("info", "hello")
	if err := bindNodeLogExecutionAttempt(log, 7); !errors.Is(err, bindErr) {
		t.Fatalf("bind error = %v, want %v", err, bindErr)
	}
	if err := flushNodeLogExecutionAttempt(log); !errors.Is(err, flushErr) {
		t.Fatalf("flush error = %v, want %v", err, flushErr)
	}
	if err := nodeLogFatal(log); !errors.Is(err, fatalErr) {
		t.Fatalf("fatal error = %v, want %v", err, fatalErr)
	}
	if count, reason := nodeLogDrops(log); count != 3 || reason != "backend unavailable" {
		t.Fatalf("drops = (%d, %q), want (3, backend unavailable)", count, reason)
	}
	if err := log.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want %v", err, closeErr)
	}
	if inner.bound != 7 || inner.flush != 1 || inner.closed != 1 ||
		len(inner.lines) != 1 || inner.lines[0] != "info:hello" {
		t.Fatalf("underlying lifecycle = %+v", inner)
	}
}
