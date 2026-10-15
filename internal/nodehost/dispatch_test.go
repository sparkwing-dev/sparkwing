package nodehost

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func hostDispatched(t *testing.T, ctx context.Context, st *store.Store, doc nodeproto.PlanDocOld, dispatch Executor) RunResult {
	t.Helper()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dispatch: dispatch, ClaimedRowPoll: 5 * time.Millisecond}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	res := RunResult{RunID: testRunID, Nodes: map[string]Result{}}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatalf("add: %v", err)
	}
	s.loop(ctx)
	res.Status, res.Err = s.verdict(ctx)
	return res
}

// hack: a stand-in pod, which starts and finishes the node's row itself, as a node host under its own claim does.
func remoteHost(st *store.Store, outcome sparkwing.Outcome, errMsg string, output []byte) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		if err := st.StartNode(ctx, n.Run.ID, n.Plan.ID); err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		if err := st.FinishNodeWithReason(ctx, n.Run.ID, n.Plan.ID, string(outcome), errMsg, output, store.FailureUnknown, nil); err != nil {
			return Result{Outcome: sparkwing.Failed, Err: err}
		}
		return Result{Outcome: sparkwing.Failed, Err: errors.New("the dispatcher's own view, which the row overrides")}
	}
}

// The row the remote host wrote is the node's result: the run host neither
// rewrites it nor trusts the dispatcher's own reading over it, and a
// dependent sees the outcome the row holds.
func TestDispatchAdoptsTheRowTheRemoteHostWrote(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var downstream atomic.Bool
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "build"}, {ID: "test", Deps: deps("build")},
	}}, func(ctx context.Context, n *NodeRun) Result {
		if n.Plan.ID == "test" {
			downstream.Store(true)
		}
		return remoteHost(st, sparkwing.Success, "", []byte(`{"digest":"abc"}`))(ctx, n)
	})
	if res.Status != "success" || !downstream.Load() {
		t.Fatalf("run = %+v (downstream ran %v), want success with test dispatched after build", res, downstream.Load())
	}
	row := nodeRow(t, st, "build")
	if row.Outcome != string(sparkwing.Success) || row.Error != "" || row.OutputRef == nil {
		t.Fatalf("build row = %+v, want the remote host's success and output left as written", row)
	}
}

// A node the remote host never finished, such as one refused before its
// claim, is finished by the run host with the dispatcher's result.
func TestDispatchFinishesARowTheRemoteHostNeverWrote(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "build"}}},
		func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("claim refused: credits exhausted")}
		})
	if res.Status != "failed" {
		t.Fatalf("run = %+v, want failed", res)
	}
	wantRow(t, st, "build", sparkwing.Failed, "claim refused")
}

// safety: retries and timeouts belong to the host that runs the node, so the
// run host dispatches a node once however many attempts it declares; a
// second dispatch would race the first pod for the same claim.
func TestDispatchSendsANodeOnceWhateverItsRetries(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	ctx := t.Context()
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{
		ID: "flaky", Modifiers: &nodeproto.PlanModifiers{Retry: 3, TimeoutMS: 1},
	}}}, func(ctx context.Context, n *NodeRun) Result {
		calls.Add(1)
		// safety: the remote host owns the node's timeout, so the run host's context carries no deadline of its own.
		if _, bounded := ctx.Deadline(); bounded {
			t.Error("the run host bounded a dispatched node by its timeout")
		}
		return remoteHost(st, sparkwing.Failed, "still broken", nil)(ctx, n)
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("dispatched %d times, want once", got)
	}
	if res.Status != "failed" {
		t.Fatalf("run = %+v, want failed", res)
	}
	wantRow(t, st, "flaky", sparkwing.Failed, "still broken")
}

// A run cancelled while a node is out stops the dispatch, and the node that
// never reached a remote host is recorded cancelled; its dependents never go.
func TestDispatchCancelledRunCancelsTheOutstandingNode(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	out := make(chan struct{})
	var downstream atomic.Bool
	go func() {
		<-out
		cancel(&lease.CancelError{Reason: "cancelled by operator"})
	}()
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{
		{ID: "build"}, {ID: "test", Deps: deps("build")},
	}}, func(ctx context.Context, n *NodeRun) Result {
		if n.Plan.ID == "test" {
			downstream.Store(true)
		}
		close(out)
		<-ctx.Done()
		return Result{Outcome: sparkwing.Cancelled, Err: ctx.Err()}
	})
	if res.Status != "cancelled" || downstream.Load() {
		t.Fatalf("run = %+v (downstream dispatched %v), want cancelled with test never dispatched", res, downstream.Load())
	}
	wantRow(t, st, "build", sparkwing.Cancelled, "")
	wantRow(t, st, "test", sparkwing.Cancelled, "")
}

// safety: a row another holder claims is that holder's to finish, so the run
// host waits for that holder's result and takes it as the node's, rather than
// fail the run with its own refused dispatch.
func TestDispatchAdoptsTheResultOfTheHolderThatWonTheClaim(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "build"}}},
		func(ctx context.Context, n *NodeRun) Result {
			won, err := st.ClaimNamedNode(ctx, store.ClaimIdentity{}, n.Run.ID, n.Plan.ID, "pool:other", time.Minute, store.NamedClaimOptions{})
			if err != nil {
				t.Errorf("claim: %v", err)
				return Result{Outcome: sparkwing.Failed, Err: err}
			}
			fenced := store.WithNodeClaimFence(context.WithoutCancel(ctx), store.NodeClaimFence{
				HolderID: won.ClaimedBy, MembershipID: won.ClaimMembershipID, ReservationID: won.ReservationID, ClaimGeneration: won.ClaimGeneration,
			})
			// safety: the holder may finish after the refused dispatch returns, so the host waits for its result.
			go func() {
				finished <- st.FinishNodeWithReason(fenced, n.Run.ID, n.Plan.ID, string(sparkwing.Success), "", nil, "", nil)
			}()
			return Result{Outcome: sparkwing.Failed, Err: store.ErrLockHeld}
		})
	if err := <-finished; err != nil {
		t.Fatalf("the holder's finish: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("run = %+v, want the holder's success", res)
	}
	if row := nodeRow(t, st, "build"); row.Outcome != string(sparkwing.Success) || row.ClaimedBy != "pool:other" {
		t.Fatalf("build row = %s/%s claimed by %q, want the holder's success", row.Status, row.Outcome, row.ClaimedBy)
	}
}

// safety: a holder that died leaves a claim whose lease lapses, and without a
// reaper nothing clears it, so the run host stops waiting once the lease is
// gone rather than block the run forever.
func TestDispatchStopsWaitingOnAHolderWhoseLeaseLapsed(t *testing.T) {
	st := openStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	res := hostDispatched(t, ctx, st, nodeproto.PlanDocOld{Nodes: []nodeproto.PlanNode{{ID: "build"}}},
		func(ctx context.Context, n *NodeRun) Result {
			if _, err := st.ClaimNamedNode(ctx, store.ClaimIdentity{}, n.Run.ID, n.Plan.ID, "pool:dead", time.Millisecond, store.NamedClaimOptions{}); err != nil {
				t.Errorf("claim: %v", err)
			}
			return Result{Outcome: sparkwing.Failed, Err: store.ErrLockHeld}
		})
	if ctx.Err() != nil || res.Status != "failed" {
		t.Fatalf("run = %+v (ctx %v), want it failed without waiting out the test", res, ctx.Err())
	}
}

// safety: a resumed run keeps an earlier host's result only for the node it
// plans now; a node planned differently fails the run as plan drift before any
// row is written, so a live holder's row and a lost node's reason stand.
func TestResumeRefusesARowPlannedUnderAnotherSpec(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.Node{
		{RunID: testRunID, NodeID: "held", Status: "pending", SpecHash: "sha256:held"},
		{RunID: testRunID, NodeID: "lost", Status: "pending", SpecHash: "sha256:lost"},
		{RunID: testRunID, NodeID: "build", Status: "pending", SpecHash: "sha256:old"},
	} {
		if err := st.CreateNode(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ClaimNamedNode(ctx, store.ClaimIdentity{}, testRunID, "held", "pool:live", time.Hour, store.NamedClaimOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "lost", "failed", "holder lost", nil, store.FailureAgentLost, nil); err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: testRunID, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	s := &scheduler{run: run, results: map[string]Result{}, started: map[string]bool{}, resume: true}
	err := s.add(ctx, ctx, []nodeproto.PlanNode{
		{ID: "held", SpecHash: "sha256:held"}, {ID: "lost", SpecHash: "sha256:lost"}, {ID: "build", SpecHash: "sha256:new"},
	}, nil)
	var perr *nodeproto.Error
	if !errors.As(err, &perr) || perr.Code != nodeproto.CodePlanDrift {
		t.Fatalf("add = %v, want plan_drift", err)
	}
	if row := nodeRow(t, st, "held"); row.Status == "done" || row.ClaimedBy != "pool:live" {
		t.Errorf("held = %s/%s claimed by %q, want left to its live holder", row.Status, row.Outcome, row.ClaimedBy)
	}
	if row := nodeRow(t, st, "lost"); row.FailureReason != store.FailureAgentLost {
		t.Errorf("lost = %s/%s (%s), want its agent_lost record kept", row.Status, row.Outcome, row.FailureReason)
	}
}

// safety: a resumed node's claim is what keeps it from running twice, so a
// host that dispatches nothing cannot resume.
func TestResumeNeedsDispatch(t *testing.T) {
	_, err := Execute(t.Context(), Config{Run: nodeproto.RunContextOld{RunID: testRunID}, Resume: true, State: openStore(t)})
	if err == nil || !strings.Contains(err.Error(), "Resume needs Dispatch") {
		t.Fatalf("Execute = %v, want Resume refused without Dispatch", err)
	}
}
