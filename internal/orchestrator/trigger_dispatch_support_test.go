package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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

// TestRunAndAwaitRefusesAnObjectStoreStateBackend pins the refusal a spawning
// node gets under Mode 2. The object-store backend enqueues a trigger and has
// no claim path, so awaiting the child would wait on a run nothing starts.
func TestRunAndAwaitRefusesAnObjectStoreStateBackend(t *testing.T) {
	s := &dispatchState{backends: Backends{State: s3StateAdapter{}}}

	resolved, err := s.pipelineAwaiter().Await(context.Background(), sparkwing.AwaitRequest{
		Pipeline: "child",
		NodeID:   "out",
	})
	if err == nil {
		t.Fatalf("expected a refusal, got %+v", resolved)
	}
	if !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("error does not name the trigger refusal: %v", err)
	}
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Errorf("error does not wrap storage.ErrNotSupported: %v", err)
	}
}

// TestCheckTriggerDispatchSeesThroughTheLocalMirror pins the ordinary Mode 2
// shape: a profile leaves mirror_local on, so the object-store backend arrives
// wrapped and an unwrapped type test would let the await through.
func TestCheckTriggerDispatchSeesThroughTheLocalMirror(t *testing.T) {
	wrapped := Backends{State: newMirrorStateBackend(s3StateAdapter{}, nil, nil)}
	if err := wrapped.checkTriggerDispatch(); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("a mirrored object-store backend was not refused: %v", err)
	}
}

// TestObjectStoreEnqueueRefusesTriggers covers the node that runs in its own
// process, which reaches this backend through the loopback shim rather than
// through the awaiter's guard.
func TestObjectStoreEnqueueRefusesTriggers(t *testing.T) {
	var state StateBackend = s3StateAdapter{}
	if _, err := state.EnqueueTrigger(context.Background(),
		"child", nil, "parent", "node", "", "await-pipeline", "", "", ""); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("EnqueueTrigger did not refuse: %v", err)
	}
	if _, err := enqueueTriggerWithEnv(context.Background(), state,
		"child", nil, "parent", "node", "", "await-pipeline", "", "", "", nil); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("EnqueueTriggerWithEnv did not refuse: %v", err)
	}
}

// TestCheckTriggerDispatchAllowsTheDispatchingBackends keeps the refusal narrow:
// the local store claims triggers itself, and a hosted controller claims them on
// its own side while leaving LocalCoordination false.
func TestCheckTriggerDispatchAllowsTheDispatchingBackends(t *testing.T) {
	hosted := client.New("http://controller.invalid", &http.Client{})
	for name, b := range map[string]Backends{
		"local":    {State: localState{}, LocalCoordination: true},
		"hosted":   {State: hosted},
		"api sock": {State: hosted, LocalCoordination: true},
	} {
		if err := b.checkTriggerDispatch(); err != nil {
			t.Errorf("%s: unexpected refusal: %v", name, err)
		}
	}
}
