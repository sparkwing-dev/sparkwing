package orchestrator

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestInProcessRunnerMarkFailedPersistsAfterContextCancel(t *testing.T) {
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
	r := &InProcessRunner{backends: LocalBackends(paths, st, nil)}
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

func TestInProcessRunnerRunNodeCancelledLeavesRowForTeardownClassifier(t *testing.T) {
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
		Home:   home,
		Stderr: io.Discard,
		Spawn:  func(string, string) error { return errors.New("daemon unavailable") },
	}, "", "", false, 0)

	r := &InProcessRunner{backends: LocalBackends(paths, st, nil)}
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

func TestInProcessRunnerVerifyFailurePersistsReasonAfterContextCancel(t *testing.T) {
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

	r := &InProcessRunner{backends: LocalBackends(paths, st, nil)}
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

func TestInProcessRunnerMarkFailedIfUnfinishedDoesNotOverwriteOnReadError(t *testing.T) {
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
	r := &InProcessRunner{backends: backends}
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
