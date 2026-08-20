package orchestrator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// approvePipe is a pipeline whose only node is an approval gate. The
// tests drive the gate to resolution out-of-band by poking the store.
type approvePipe struct{ sparkwing.Base }

func (approvePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.JobApproval(plan, "gate", sparkwing.ApprovalConfig{
		Message: "approve?",
		Timeout: 30 * time.Second,
	})
	return nil
}

// approveTimeoutPipe uses a tiny timeout so the waiter fires itself.
type approveTimeoutPipe struct{ sparkwing.Base }

func (approveTimeoutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.JobApproval(plan, "gate", sparkwing.ApprovalConfig{
		Message: "fast timeout",
		Timeout: 200 * time.Millisecond,
	})
	return nil
}

func init() {
	register("appr-basic", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &approvePipe{} })
	register("appr-timeout", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &approveTimeoutPipe{} })
}

func resolveNextApproval(ctx context.Context, dbPath, resolution, approver, note string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	var st *store.Store
	defer func() {
		if st != nil {
			_ = st.Close()
		}
	}()
	var lastErr error
	for {
		if st == nil {
			if _, err := os.Stat(dbPath); err == nil {
				st, lastErr = store.Open(dbPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat store: %w", err)
			}
		}
		if st != nil {
			pending, err := st.ListPendingApprovals(ctx)
			if err != nil {
				lastErr = err
			} else if len(pending) > 0 {
				a := pending[0]
				if _, err := st.ResolveApproval(ctx, a.RunID, a.NodeID, resolution, approver, note); err != nil {
					return fmt.Errorf("resolve approval: %w", err)
				}
				return nil
			}
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("wait for pending approval: %w (last store error: %v)", ctx.Err(), lastErr)
			}
			return fmt.Errorf("wait for pending approval: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

type approvalRunResult struct {
	result *orchestrator.Result
	err    error
}

func joinApprovalWorker(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Errorf("%s did not stop within 2s", name)
	}
}

func TestApproval_ApprovedFlowsToSuccess(t *testing.T) {
	p := newPaths(t)
	dbPath := filepath.Join(p.Root, "state.db")
	testCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan approvalRunResult, 1)
	runFinished := make(chan struct{})
	go func() {
		defer close(runFinished)
		res, err := orchestrator.RunLocal(testCtx, p,
			orchestrator.Options{Pipeline: "appr-basic"})
		done <- approvalRunResult{result: res, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		joinApprovalWorker(t, "approval run", runFinished)
	})

	resolverDone := make(chan error, 1)
	resolverFinished := make(chan struct{})
	go func() {
		defer close(resolverFinished)
		ctx, stopResolver := context.WithTimeout(testCtx, 10*time.Second)
		defer stopResolver()
		resolverDone <- resolveNextApproval(ctx, dbPath, store.ApprovalResolutionApproved, "alice", "ok")
	}()
	t.Cleanup(func() {
		cancel()
		joinApprovalWorker(t, "approval resolver", resolverFinished)
	})

	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("Run: %v", outcome.err)
		}
		res := outcome.result
		if res == nil {
			t.Fatal("nil result")
		}
		if res.Status != "success" {
			t.Fatalf("status = %q, want success", res.Status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not complete within 15s")
	}
	if err := <-resolverDone; err != nil {
		t.Fatalf("resolver: %v", err)
	}

	st, _ := store.Open(dbPath)
	defer func() { _ = st.Close() }()
	runs, _ := st.ListRuns(context.Background(), store.RunFilter{Pipelines: []string{"appr-basic"}, Limit: 1})
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	appr, err := st.GetApproval(context.Background(), runs[0].ID, "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if appr.Resolution != store.ApprovalResolutionApproved {
		t.Fatalf("resolution: %q", appr.Resolution)
	}
	if appr.Approver != "alice" {
		t.Fatalf("approver: %q", appr.Approver)
	}
	nodes, _ := st.ListNodes(context.Background(), runs[0].ID)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != "success" {
		t.Fatalf("node outcome: %q", nodes[0].Outcome)
	}
}

func TestApproval_DeniedFlowsToFailed(t *testing.T) {
	p := newPaths(t)
	dbPath := filepath.Join(p.Root, "state.db")
	testCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan approvalRunResult, 1)
	runFinished := make(chan struct{})
	go func() {
		defer close(runFinished)
		res, err := orchestrator.RunLocal(testCtx, p,
			orchestrator.Options{Pipeline: "appr-basic"})
		done <- approvalRunResult{result: res, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		joinApprovalWorker(t, "approval run", runFinished)
	})

	resolverDone := make(chan error, 1)
	resolverFinished := make(chan struct{})
	go func() {
		defer close(resolverFinished)
		ctx, stopResolver := context.WithTimeout(testCtx, 4*time.Second)
		defer stopResolver()
		resolverDone <- resolveNextApproval(ctx, dbPath, store.ApprovalResolutionDenied, "bob", "no go")
	}()
	t.Cleanup(func() {
		cancel()
		joinApprovalWorker(t, "approval resolver", resolverFinished)
	})

	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("Run: %v", outcome.err)
		}
		res := outcome.result
		if res == nil {
			t.Fatal("nil result")
		}
		if res.Status != "failed" {
			t.Fatalf("status = %q, want failed", res.Status)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("run did not complete within 6s")
	}
	if err := <-resolverDone; err != nil {
		t.Fatalf("resolver: %v", err)
	}
}

func TestApproval_TimeoutWithPolicyFail(t *testing.T) {
	p := newPaths(t)
	start := time.Now()
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "appr-timeout"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("run took too long: %v", time.Since(start))
	}

	dbPath := filepath.Join(p.Root, "state.db")
	st, _ := store.Open(dbPath)
	defer func() { _ = st.Close() }()
	appr, err := st.GetApproval(context.Background(), res.RunID, "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if appr.Resolution != store.ApprovalResolutionTimedOut {
		t.Fatalf("resolution: %q", appr.Resolution)
	}
}
