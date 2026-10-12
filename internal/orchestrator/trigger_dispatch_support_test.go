package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type progressCheckingTriggerState struct {
	StateBackend
	paused  bool
	expired bool
}

func (s *progressCheckingTriggerState) EnqueueTrigger(
	ctx context.Context, _ string, _ map[string]string, _, _, _, _, _, _, _ string,
) (string, error) {
	s.paused = ProgressTimeoutPausedForTest(ctx)
	s.expired = ExpireProgressTimeoutForTest(ctx)
	return "", errors.New("stop after observing trigger enqueue")
}

func TestRunAndAwaitPausesProgressTimeoutBeforeLocalTriggerEnqueue(t *testing.T) {
	state := &progressCheckingTriggerState{}
	dispatch := &dispatchState{backends: Backends{State: state}, runID: "parent"}
	ctx, _, cancel := newProgressTimeoutContext(context.Background(), time.Hour)
	defer cancel()

	_, err := dispatch.pipelineAwaiter().Await(ctx, sparkwing.AwaitRequest{Pipeline: "child"})
	if err == nil {
		t.Fatal("RunAndAwait passed despite the trigger enqueue failure")
	}
	if !state.paused {
		t.Fatal("progress timeout was not paused during local trigger enqueue")
	}
	if state.expired {
		t.Fatal("progress timeout fired during local trigger enqueue")
	}
	if !ExpireProgressTimeoutForTest(ctx) {
		t.Fatal("progress timeout did not resume after trigger enqueue returned")
	}
}

func TestChildAwaitOwnerPausesBeforePreparingTriggerEnvironment(t *testing.T) {
	state := &progressCheckingTriggerState{}
	ctx, _, cancel := newProgressTimeoutContext(context.Background(), time.Hour)
	defer cancel()
	preparedWhilePaused := false
	config := childAwaitConfig{
		state:       state,
		parentRunID: "parent",
		masker:      secrets.NewMasker(),
		triggerEnv: func(ctx context.Context) map[string]string {
			preparedWhilePaused = ProgressTimeoutPausedForTest(ctx)
			return nil
		},
	}

	_, err := config.await(ctx, sparkwing.AwaitRequest{Pipeline: "child"})
	if err == nil {
		t.Fatal("child await passed despite the trigger enqueue failure")
	}
	if !preparedWhilePaused {
		t.Fatal("trigger environment was prepared before the progress timeout paused")
	}
	if !ExpireProgressTimeoutForTest(ctx) {
		t.Fatal("progress timeout did not resume after child setup returned")
	}
}

type auditFailingChildState struct {
	StateBackend
}

func (*auditFailingChildState) EnqueueTrigger(
	context.Context, string, map[string]string, string, string, string, string, string, string, string,
) (string, error) {
	return "child", nil
}

func (*auditFailingChildState) AppendEvent(context.Context, string, string, string, []byte) error {
	return errors.New("audit unavailable")
}

func (*auditFailingChildState) GetRun(context.Context, string) (*store.Run, error) {
	return &store.Run{ID: "child", Status: "success"}, nil
}

func TestChildAwaitOwnerKeepsAuditWritesBestEffort(t *testing.T) {
	var logs bytes.Buffer
	diagnostics := podChildAwaitDiagnostics{
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	config := childAwaitConfig{
		state:       &auditFailingChildState{},
		parentRunID: "parent",
		masker:      secrets.NewMasker(),
		diagnostics: diagnostics,
		pollFactory: func() (childAwaitPollPolicy, error) {
			return &retryChildAwaitPoll{diagnostics: diagnostics}, nil
		},
	}
	ctx := sparkwingruntime.WithNode(context.Background(), "parent-node")

	resolved, err := config.await(ctx, sparkwing.AwaitRequest{Pipeline: "child"})
	if err != nil {
		t.Fatalf("audit failure changed child outcome: %v", err)
	}
	if resolved.RunID != "child" {
		t.Fatalf("resolved run = %q, want child", resolved.RunID)
	}
	got := logs.String()
	if count := strings.Count(got, `"msg":"child run audit event append failed"`); count != 2 {
		t.Fatalf("audit warning count = %d, want start and finish\n%s", count, got)
	}
	for _, field := range []string{
		`"write":"child_run_start"`, `"write":"child_run_finish"`,
		`"run_id":"parent"`, `"node":"parent-node"`, `"err":"audit unavailable"`,
	} {
		if !strings.Contains(got, field) {
			t.Errorf("audit warnings missing %s\n%s", field, got)
		}
	}
}

func TestPodChildAwaitPollLogsFirstFailureWithIdentity(t *testing.T) {
	var logs bytes.Buffer
	diagnostics := podChildAwaitDiagnostics{
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	policy := &retryChildAwaitPoll{
		diagnostics: diagnostics,
		runID:       "parent",
		nodeID:      "parent-node",
	}
	pollErr := errors.New("controller unavailable")

	if err := policy.failure(t.Context(), "child", "deploy", pollErr, true); err != nil {
		t.Fatalf("first retryable failure became terminal: %v", err)
	}
	if err := policy.failure(t.Context(), "child", "deploy", pollErr, false); err != nil {
		t.Fatalf("later retryable failure became terminal: %v", err)
	}

	got := logs.String()
	if count := strings.Count(got, `"msg":"child run status poll failed; retrying"`); count != 1 {
		t.Fatalf("poll warning count = %d, want one\n%s", count, got)
	}
	for _, field := range []string{
		`"run_id":"parent"`, `"node":"parent-node"`,
		`"child_run_id":"child"`, `"pipeline":"deploy"`, `"err":"controller unavailable"`,
	} {
		if !strings.Contains(got, field) {
			t.Errorf("poll warning missing %s\n%s", field, got)
		}
	}
}

type childAwaitRecordLogger struct {
	records []sparkwing.LogRecord
}

func (l *childAwaitRecordLogger) Log(level, message string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: message})
}

func (l *childAwaitRecordLogger) Emit(record sparkwing.LogRecord) {
	l.records = append(l.records, record)
}

func TestLocalChildAwaitDiagnosticsRetainsExecutionEnvelope(t *testing.T) {
	log := &childAwaitRecordLogger{}
	ctx := sparkwingruntime.WithLogger(t.Context(), log)
	ctx = sparkwingruntime.WithNode(ctx, "parent-node")
	ctx = sparkwing.WithStep(ctx, "deploy")
	diagnostics := localChildAwaitDiagnostics{}

	diagnostics.info(ctx, "child run admitted", "child_run_id", "child")
	diagnostics.warn(ctx, "child poll failed", errors.New("store unavailable"),
		"child_run_id", "child", "pipeline", "release")

	if len(log.records) != 2 {
		t.Fatalf("records = %d, want info and warning", len(log.records))
	}
	for _, record := range log.records {
		if record.TS.IsZero() || record.JobID != "parent-node" || record.Step != "deploy" {
			t.Errorf("record envelope = %+v, want timestamp and parent-node/deploy", record)
		}
		if record.Attrs["child_run_id"] != "child" {
			t.Errorf("record attrs = %v, want child identity", record.Attrs)
		}
	}
	warning := log.records[1]
	if warning.Level != "warn" || warning.Attrs["pipeline"] != "release" || warning.Attrs["error"] != "store unavailable" {
		t.Fatalf("warning = %+v, want structured pipeline and error", warning)
	}
}
