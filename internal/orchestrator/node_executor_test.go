package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type acknowledgingState struct {
	StateBackend
	mu     sync.Mutex
	starts int
	holder string
	order  *[]string
}

type executionBoundaryLog struct {
	mu    *sync.Mutex
	order *[]string
}

func (l *executionBoundaryLog) Log(string, string)           {}
func (l *executionBoundaryLog) Emit(sparkwing.LogRecord)     {}
func (l *executionBoundaryLog) Close() error                 { return nil }
func (l *executionBoundaryLog) FlushExecutionAttempt() error { return nil }
func (l *executionBoundaryLog) BindExecutionAttempt(int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.order = append(*l.order, "bind")
	return nil
}

type executionBoundaryLogs struct{ log NodeLog }

func (l executionBoundaryLogs) OpenNodeLog(context.Context, string, string, sparkwing.Logger) (NodeLog, error) {
	return l.log, nil
}

type rejectingSecondAttemptState struct {
	StateBackend
	ack executionStartAcknowledger
}

type stateWithoutExecutionRecorder struct{ StateBackend }

func (s *rejectingSecondAttemptState) AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error {
	if start.AttemptOrdinal == 2 {
		return store.ErrLockHeld
	}
	return s.ack.AcknowledgeNodeExecutionStart(ctx, runID, nodeID, start)
}

func (s *rejectingSecondAttemptState) FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error {
	return s.ack.FinishNodeExecutionAttempt(ctx, runID, nodeID, finish)
}

func (s *acknowledgingState) AcknowledgeNodeExecutionStart(ctx context.Context, runID, nodeID string, start store.ExecutionStart) error {
	s.mu.Lock()
	s.starts++
	s.holder = start.HolderID
	if s.order != nil {
		*s.order = append(*s.order, "ack")
	}
	s.mu.Unlock()
	return s.StateBackend.(executionStartAcknowledger).AcknowledgeNodeExecutionStart(ctx, runID, nodeID, start)
}

func (s *acknowledgingState) FinishNodeExecutionAttempt(ctx context.Context, runID, nodeID string, finish store.ExecutionAttemptFinish) error {
	return s.StateBackend.(executionStartAcknowledger).FinishNodeExecutionAttempt(ctx, runID, nodeID, finish)
}

func TestNodeExecutorMarkFailedPersistsAfterContextCancel(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-cancelled-terminal-write",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-cancelled-terminal-write",
		NodeID: "node",
		Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r := &NodeExecutor{backends: LocalBackends(paths, st, nil)}
	r.markFailed(cancelled, "run-cancelled-terminal-write", "node", errors.New("local admission failed"))

	node, err := st.GetNode(ctx, "run-cancelled-terminal-write", "node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", node.Outcome)
	}
	if node.Error != "local admission failed" {
		t.Fatalf("node error = %q, want local admission failed", node.Error)
	}
}

func TestClaimedNodeAcknowledgesAtExecutionBoundary(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "build"); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "gateway:edge-a:1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	var order []string
	state := &acknowledgingState{StateBackend: backends.State, order: &order}
	backends.State = state
	backends.Logs = executionBoundaryLogs{log: &executionBoundaryLog{mu: &state.mu, order: &order}}
	plan := sparkwing.NewPlan()
	ran := false
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		order = append(order, "body")
		stored, err := st.GetNode(context.Background(), "run-1", "build")
		if err != nil {
			t.Fatal(err)
		}
		if state.starts != 1 || stored.StartedAt == nil {
			t.Fatalf("body saw execution acknowledgements=%d started_at=%v", state.starts, stored.StartedAt)
		}
		ran = true
		return nil
	})
	exec := NewNodeExecutor(backends)
	ctx = withNodeClaimHolder(ctx, "gateway:edge-a:1")
	ctx = store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: claimed.ClaimedBy, ClaimGeneration: claimed.ClaimGeneration,
	})
	if _, err := exec.executeNodeInProcess(ctx, "run-1", node, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ran || state.holder != "gateway:edge-a:1" {
		t.Fatalf("ran=%v holder=%q", ran, state.holder)
	}
	if got := strings.Join(order, ","); got != "bind,ack,body" {
		t.Fatalf("execution boundary order = %q, want bind,ack,body", got)
	}
}

func TestTriggerOwnedNodeConsumesCarriedExecutionOrdinal(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-trigger-body", Pipeline: "test", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	})
	ctx = store.WithExecutionAttemptOrdinal(ctx, 1)
	if err := st.CreateRun(ctx, store.Run{ID: trigger.ID, Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	invocations := 0
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		invocations++
		stored, getErr := st.GetNode(context.Background(), trigger.ID, "build")
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.AttemptsConsumed != 1 {
			t.Fatalf("body observed attempts_consumed=%d, want 1", stored.AttemptsConsumed)
		}
		return nil
	})
	if _, err := NewNodeExecutor(LocalBackends(paths, st, nil)).executeNodeInProcess(ctx, trigger.ID, node, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetNode(context.Background(), trigger.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if invocations != 1 || stored.AttemptsConsumed != 1 || len(stored.ExecutionAttempts) != 1 ||
		stored.ExecutionAttempts[0].Outcome != "success" {
		t.Fatalf("invocations=%d node=%+v", invocations, stored)
	}
}

func TestTriggerOwnedNodeDoesNotRunWithoutExecutionRecorder(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-trigger-no-recorder", Pipeline: "test", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := st.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{
		Claimant: identity, ClaimGeneration: trigger.ClaimSeq,
	})
	if err := st.CreateRun(ctx, store.Run{ID: trigger.ID, Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	backends.State = stateWithoutExecutionRecorder{StateBackend: backends.State}
	invocations := 0
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		invocations++
		return nil
	})
	if _, err := NewNodeExecutor(backends).executeNodeInProcess(ctx, trigger.ID, node, nil); err == nil ||
		!strings.Contains(err.Error(), "does not support execution-attempt acknowledgement") {
		t.Fatalf("execute = %v, want unsupported recorder error", err)
	}
	if invocations != 0 {
		t.Fatalf("job-body invocations = %d, want zero", invocations)
	}
}

func TestClaimedNodeDoesNotInvokeBodyWhenNextOrdinalIsRejected(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-budget", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-budget", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-budget", "build"); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:desk:1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	backends := LocalBackends(paths, st, nil)
	state := &rejectingSecondAttemptState{StateBackend: backends.State, ack: backends.State.(executionStartAcknowledger)}
	backends.State = state
	plan := sparkwing.NewPlan()
	invocations := 0
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		invocations++
		return errors.New("retry me")
	}).Retry(1)
	ctx = withNodeClaimHolder(ctx, claimed.ClaimedBy)
	ctx = store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: claimed.ClaimedBy, ClaimGeneration: claimed.ClaimGeneration,
	})
	if _, err := NewNodeExecutor(backends).executeNodeInProcess(ctx, "run-budget", node, nil); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("execute = %v, want ErrLockHeld", err)
	}
	if invocations != 1 {
		t.Fatalf("job-body invocations = %d, want 1", invocations)
	}
	stored, err := st.GetNode(context.Background(), "run-budget", "build")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AttemptsConsumed != 1 || len(stored.ExecutionAttempts) != 1 {
		t.Fatalf("stored attempts = %+v", stored)
	}
}

func TestClaimedNodeCancellationFinishesAcknowledgedAttempt(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	if err := st.CreateRun(ctx, store.Run{ID: "run-cancelled-attempt", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-cancelled-attempt", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-cancelled-attempt", "build"); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:desk:1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "build", func(bodyCtx context.Context) error {
		cancel()
		return bodyCtx.Err()
	})
	claimCtx := withNodeClaimHolder(ctx, claimed.ClaimedBy)
	claimCtx = store.WithNodeClaimFence(claimCtx, store.NodeClaimFence{
		HolderID: claimed.ClaimedBy, ClaimGeneration: claimed.ClaimGeneration,
	})
	if _, err := NewNodeExecutor(LocalBackends(paths, st, nil)).executeNodeInProcess(claimCtx, claimed.RunID, node, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("execute = %v, want context canceled", err)
	}
	stored, err := st.GetNode(context.Background(), claimed.RunID, claimed.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.ExecutionAttempts) != 1 || stored.ExecutionAttempts[0].FinishedAt == nil ||
		stored.ExecutionAttempts[0].Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("cancelled attempt = %+v", stored.ExecutionAttempts)
	}
}

func TestNodeExecutorSubtractsCarriedAttemptsFromLocalRetryBudget(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-carried-budget", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-carried-budget", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET attempts_consumed = 1 WHERE run_id = ? AND node_id = ?`, "run-carried-budget", "build"); err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	invocations := 0
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		invocations++
		return errors.New("failed")
	}).Retry(2)
	if _, err := NewNodeExecutor(LocalBackends(paths, st, nil)).executeNodeInProcess(ctx, "run-carried-budget", node, nil); err == nil {
		t.Fatal("execute succeeded, want failure")
	}
	if invocations != 2 {
		t.Fatalf("job-body invocations = %d, want two remaining invocations", invocations)
	}
	events, err := st.ListEventsAfter(ctx, "run-carried-budget", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var sawGlobalOrdinal bool
	for _, event := range events {
		if event.Kind == "attempt_retry" && strings.Contains(string(event.Payload), "attempt 3/3") {
			sawGlobalOrdinal = true
		}
	}
	if !sawGlobalOrdinal {
		t.Fatalf("retry events do not identify global attempt 3/3: %+v", events)
	}
}

func TestClaimedRetryAutoReportsFailureWithoutPrivateRedispatch(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-auto-budget", Pipeline: "test", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-auto-budget", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-auto-budget", "build"); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, "agent:desk:1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := sparkwing.NewPlan()
	invocations := 0
	node := sparkwing.Job(plan, "build", func(context.Context) error {
		invocations++
		return errors.New("retry me")
	}).Retry(2, sparkwing.RetryAuto())
	ctx = withNodeClaimHolder(ctx, claimed.ClaimedBy)
	ctx = store.WithNodeClaimFence(ctx, store.NodeClaimFence{
		HolderID: claimed.ClaimedBy, ClaimGeneration: claimed.ClaimGeneration,
	})
	exec := NewNodeExecutor(LocalBackends(paths, st, nil))
	result := exec.RunNode(ctx, runner.Request{RunID: "run-auto-budget", NodeID: "build", Node: node})
	if result.Outcome != sparkwing.Failed || result.Err == nil {
		t.Fatalf("result = %+v, want reported failure", result)
	}
	stored, err := st.GetNode(ctx, "run-auto-budget", "build")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AttemptsConsumed != 1 || len(stored.ExecutionAttempts) != 1 || stored.Outcome != string(sparkwing.Failed) {
		t.Fatalf("stored node = %+v", stored)
	}
	exec.RunNode(ctx, runner.Request{RunID: "run-auto-budget", NodeID: "build", Node: node})
	if invocations != 1 {
		t.Fatalf("job-body invocations = %d, want one; claimed workers must not privately re-dispatch RetryAuto", invocations)
	}
}

func TestNodeExecutorRunNodeCancelledLeavesRowForTeardownClassifier(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-cancelled-no-terminal-write",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "node", func(context.Context) error { return nil })
	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-cancelled-no-terminal-write",
		NodeID: "node",
		Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cancelled = withLocalAdmission(cancelled, &LocalAdmission{
		Home:  home,
		Out:   io.Discard,
		Spawn: func(string, string) error { return errors.New("daemon unavailable") },
	}, "", "", false, 0)

	r := &NodeExecutor{backends: LocalBackends(paths, st, nil)}
	res := r.RunNode(cancelled, runner.Request{
		RunID:    "run-cancelled-no-terminal-write",
		NodeID:   "node",
		Pipeline: "test",
		Node:     node,
	})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("RunNode outcome = %q, want failed result surfaced to the dispatcher", res.Outcome)
	}

	stored, err := st.GetNode(ctx, "run-cancelled-no-terminal-write", "node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if stored.Outcome != "" {
		t.Fatalf("stored outcome = %q, want unfinished (teardown classifies cancelled nodes)", stored.Outcome)
	}
}

func TestNodeExecutorVerifyFailurePersistsReasonAfterContextCancel(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	if err := st.CreateRun(context.Background(), store.Run{
		ID:        "run-verify-terminal-write",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	plan := sparkwing.NewPlan()
	node := sparkwing.Job(plan, "node", func(context.Context) error { return nil }).
		Verify(func(context.Context) error {
			cancel()
			return errors.New("postcondition failed")
		})
	if err := st.CreateNode(context.Background(), store.Node{
		RunID:  "run-verify-terminal-write",
		NodeID: "node",
		Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	r := &NodeExecutor{backends: LocalBackends(paths, st, nil)}
	res := r.RunNode(ctx, runner.Request{
		RunID:    "run-verify-terminal-write",
		NodeID:   "node",
		Pipeline: "test",
		Node:     node,
	})
	if res.Outcome != sparkwing.Failed {
		t.Fatalf("RunNode outcome = %q, want failed", res.Outcome)
	}

	stored, err := st.GetNode(context.Background(), "run-verify-terminal-write", "node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if stored.FailureReason != store.FailureVerify {
		t.Fatalf("failure_reason = %q, want %q", stored.FailureReason, store.FailureVerify)
	}
	if stored.Outcome != string(sparkwing.Failed) {
		t.Fatalf("stored outcome = %q, want failed", stored.Outcome)
	}
}

func TestNodeExecutorMarkFailedIfUnfinishedDoesNotOverwriteOnReadError(t *testing.T) {
	home := t.TempDir()
	paths := PathsAt(home)
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-read-error-terminal-write",
		Pipeline:  "test",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID:  "run-read-error-terminal-write",
		NodeID: "node",
		Status: "pending",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.FinishNodeWithReason(ctx, "run-read-error-terminal-write", "node",
		string(sparkwing.Failed), "verify: failed", nil, store.FailureVerify, nil); err != nil {
		t.Fatalf("seed terminal node: %v", err)
	}

	backends := LocalBackends(paths, st, nil)
	backends.State = getNodeErrorState{StateBackend: backends.State}
	r := &NodeExecutor{backends: backends}
	r.markFailedIfUnfinished(ctx, "run-read-error-terminal-write", "node", errors.New("generic failure"))

	stored, err := st.GetNode(ctx, "run-read-error-terminal-write", "node")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if stored.FailureReason != store.FailureVerify {
		t.Fatalf("failure_reason = %q, want %q", stored.FailureReason, store.FailureVerify)
	}
	if stored.Error != "verify: failed" {
		t.Fatalf("error = %q, want original verify failure", stored.Error)
	}
}

type getNodeErrorState struct {
	StateBackend
}

func (s getNodeErrorState) GetNode(context.Context, string, string) (*store.Node, error) {
	return nil, errors.New("read failed")
}
