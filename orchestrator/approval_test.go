package orchestrator_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// approvePipe is a pipeline whose only node is an approval gate. The
// tests drive the gate to resolution out-of-band by poking the store.
type approvePipe struct{ sparkwing.Base }

func (approvePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.JobApproval(plan, "gate", sparkwing.ApprovalConfig{
		Message: "approve?",
		Timeout: 5 * time.Second,
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

func TestApproval_ApprovedFlowsToSuccess(t *testing.T) {
	p := newPaths(t)
	dbPath := filepath.Join(p.Root, "state.db")

	// Kick off the run on a goroutine; mark the gate approved once the
	// row appears.
	done := make(chan *orchestrator.Result, 1)
	go func() {
		res, err := orchestrator.RunLocal(context.Background(), p,
			orchestrator.Options{Pipeline: "appr-basic"})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()

	// Resolver coroutine: polls until the approval row exists, then
	// approves it. The orchestrator's 500ms waiter tick picks this up.
	// The run id isn't known up-front, so scan pending approvals.
	// A short initial sleep lets RunLocal open the DB first; SQLite
	// serializes the Open migration under single-writer and we don't
	// want the resolver's reopen-loop to starve that out.
	go func() {
		time.Sleep(200 * time.Millisecond)
		st, err := store.Open(dbPath)
		if err != nil {
			return
		}
		defer st.Close()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			pend, _ := st.ListPendingApprovals(context.Background())
			if len(pend) > 0 {
				a := pend[0]
				_, _ = st.ResolveApproval(context.Background(), a.RunID, a.NodeID,
					store.ApprovalResolutionApproved, "alice", "ok")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case res := <-done:
		if res == nil {
			t.Fatal("nil result")
		}
		if res.Status != "success" {
			t.Fatalf("status = %q, want success", res.Status)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("run did not complete within 6s")
	}

	// Confirm durable state reflects the approval.
	st, _ := store.Open(dbPath)
	defer st.Close()
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

	done := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p,
			orchestrator.Options{Pipeline: "appr-basic"})
		done <- res
	}()

	go func() {
		time.Sleep(200 * time.Millisecond)
		st, err := store.Open(dbPath)
		if err != nil {
			return
		}
		defer st.Close()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			pend, _ := st.ListPendingApprovals(context.Background())
			if len(pend) > 0 {
				a := pend[0]
				_, _ = st.ResolveApproval(context.Background(), a.RunID, a.NodeID,
					store.ApprovalResolutionDenied, "bob", "no go")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case res := <-done:
		if res.Status != "failed" {
			t.Fatalf("status = %q, want failed", res.Status)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("run did not complete within 6s")
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
	if time.Since(start) > 3*time.Second {
		t.Fatalf("run took too long: %v", time.Since(start))
	}

	dbPath := filepath.Join(p.Root, "state.db")
	st, _ := store.Open(dbPath)
	defer st.Close()
	appr, err := st.GetApproval(context.Background(), res.RunID, "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if appr.Resolution != store.ApprovalResolutionTimedOut {
		t.Fatalf("resolution: %q", appr.Resolution)
	}
}
