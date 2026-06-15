package orchestrator_test

import (
	"context"
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

func TestApproval_ApprovedFlowsToSuccess(t *testing.T) {
	p := newPaths(t)
	dbPath := filepath.Join(p.Root, "state.db")

	done := make(chan *orchestrator.Result, 1)
	go func() {
		res, err := orchestrator.RunLocal(context.Background(), p,
			orchestrator.Options{Pipeline: "appr-basic"})
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- res
	}()

	resolverDone := make(chan struct{})
	go func() {
		defer close(resolverDone)
		time.Sleep(200 * time.Millisecond)
		st, err := store.Open(dbPath)
		if err != nil {
			t.Errorf("resolver: store.Open: %v", err)
			return
		}
		defer func() { _ = st.Close() }()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			pend, listErr := st.ListPendingApprovals(context.Background())
			if listErr != nil {
				t.Errorf("resolver: ListPendingApprovals: %v", listErr)
				return
			}
			if len(pend) > 0 {
				a := pend[0]
				if _, err := st.ResolveApproval(context.Background(), a.RunID, a.NodeID,
					store.ApprovalResolutionApproved, "alice", "ok"); err != nil {
					t.Errorf("resolver: ResolveApproval: %v", err)
				}
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Errorf("resolver: no pending approval appeared within 10s")
	}()

	select {
	case res := <-done:
		if res == nil {
			t.Fatal("nil result")
		}
		if res.Status != "success" {
			t.Fatalf("status = %q, want success", res.Status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not complete within 15s")
	}
	<-resolverDone

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
		defer func() { _ = st.Close() }()
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
	defer func() { _ = st.Close() }()
	appr, err := st.GetApproval(context.Background(), res.RunID, "gate")
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if appr.Resolution != store.ApprovalResolutionTimedOut {
		t.Fatalf("resolution: %q", appr.Resolution)
	}
}
