package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func (f dispatchRun) claimRaw(t *testing.T, nodeID string, kind store.ClaimTokenKind) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	// safety: a retry waits out its backoff before the launcher may claim it,
	// and these tests drive the retry at once.
	if n := f.node(t, nodeID); n.ReadyAt != nil && n.ReadyAt.After(now) {
		now = *n.ReadyAt
	}
	c, err := f.s.ClaimLaunch(ctx, f.claimant, store.LaunchClaimRequest{
		HolderID: "holder-" + nodeID, Lease: time.Minute, Deadline: time.Hour, RunID: f.run, NodeID: nodeID,
	}, now)
	if err != nil || c == nil || c.Kind != kind {
		t.Fatalf("claim %s as %s: %+v %v", nodeID, kind, c, err)
	}
	return c.Token
}

func (f dispatchRun) authorize(t *testing.T, raw string, class store.ClaimRouteClass) (store.ClaimToken, error) {
	t.Helper()
	return f.s.AuthorizeClaimToken(context.Background(), raw, class, time.Now())
}

func (f dispatchRun) wantAdmission(t *testing.T, want string) {
	t.Helper()
	r, err := f.s.GetRun(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if r.Admission != want {
		t.Fatalf("admission = %q, want %q", r.Admission, want)
	}
}

// A cancel request needs no trigger holder: it cancels every node no claim
// holds at once, and a claimed node's late report records its true outcome
// but releases nothing downstream. Without the request the same report
// releases the dependent, which is what makes the cancelled case meaningful.
func TestCancel_LateReportAfterCancelReleasesNothing(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		name := "uncancelled"
		if cancel {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			f := newDispatchRun(t, "run-cancel-"+name)
			f.mustAccept(t, planOf("a", "b:a", "c"))
			raw := f.claimRaw(t, "a", store.ClaimTokenWork)
			if cancel {
				if err := f.s.RequestCancel(context.Background(), f.run); err != nil {
					t.Fatalf("cancel: %v", err)
				}
				f.wantOutcome(t, "b", "cancelled")
				f.wantOutcome(t, "c", "cancelled")
				f.wantRun(t, "running")
				if n := f.node(t, "a"); n.Status == "done" || n.ClaimedBy == "" {
					t.Fatalf("cancel ended a's live claim: %s claimed by %q", n.Status, n.ClaimedBy)
				}
				if _, err := f.authorize(t, raw, store.ClaimSensitive); !errors.Is(err, store.ErrClaimCancelRequested) {
					t.Fatalf("sensitive route after cancel: err = %v", err)
				}
				if _, err := f.authorize(t, raw, store.ClaimReporting); err != nil {
					t.Fatalf("reporting route after cancel: %v", err)
				}
			}
			tok, err := f.authorize(t, raw, store.ClaimResult)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.report(tok, store.AttemptReport{Outcome: "success"}); err != nil {
				t.Fatalf("late report: %v", err)
			}
			f.wantOutcome(t, "a", "success")
			if cancel {
				f.wantOutcome(t, "b", "cancelled")
				f.wantRun(t, "cancelled")
				return
			}
			f.wantReleased(t, "b", true)
		})
	}
}

// A cancel request retries nothing, and an approval gate waiting on a person
// is cancelled and resolved, so no one can approve it afterwards.
func TestCancel_StopsRetriesAndClosesOpenGates(t *testing.T) {
	f := newDispatchRun(t, "run-cancel-gate")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":3,"retry_auto":true}`, `gate|"approval":{"message":"ship?"}`))
	tok := f.claim(t, "a", store.ClaimTokenWork)
	ctx := context.Background()
	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	f.wantOutcome(t, "gate", "cancelled")
	if a, err := f.s.GetApproval(ctx, f.run, "gate"); err != nil || a.ResolvedAt == nil {
		t.Fatalf("the cancelled gate's approval is still open (%v)", err)
	}
	if _, err := f.s.ResolveApproval(ctx, f.run, "gate", store.ApprovalResolutionApproved, "korey", ""); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("approving a cancelled gate: err = %v", err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	f.wantOutcome(t, "a", "failed")
	f.wantRun(t, "cancelled")
	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatalf("a second cancel request: %v", err)
	}
}

func (f dispatchRun) expireGate(t *testing.T, nodeID string) {
	t.Helper()
	if _, err := f.s.DB().ExecContext(context.Background(), storetest.Rebind(f.s,
		`UPDATE approvals SET requested_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Hour).UnixNano(), f.run, nodeID); err != nil {
		t.Fatal(err)
	}
}

// An approval gate opens only once its upstreams succeed, and is never
// released to a claim.
func TestApproval_GateOpensAfterItsUpstream(t *testing.T) {
	f := newDispatchRun(t, "run-gate-deps")
	f.mustAccept(t, planOf("build", `gate:build|"approval":{"message":"ship?"}`, "deploy:gate"))
	if _, err := f.s.GetApproval(context.Background(), f.run, "gate"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the gate opened before its upstream finished: %v", err)
	}
	f.mustReport(t, "build", "success")
	gate := f.node(t, "gate")
	if gate.Status != store.NodeStatusApprovalPending || gate.ReadyAt != nil {
		t.Fatalf("gate = %s ready %v, want an open gate no claim can take", gate.Status, gate.ReadyAt)
	}
	a, err := f.s.GetApproval(context.Background(), f.run, "gate")
	if err != nil || a.Message != "ship?" || a.OnTimeout != store.ApprovalOnTimeoutFail {
		t.Fatalf("approval = %+v (%v)", a, err)
	}
	if _, err := f.s.ResolveApproval(context.Background(), f.run, "gate", store.ApprovalResolutionApproved, "korey", "go"); err != nil {
		t.Fatal(err)
	}
	f.wantReleased(t, "deploy", true)
}

func TestAdmission_PlanningUntilThePlanIsAccepted(t *testing.T) {
	f := newDispatchRun(t, "run-admission")
	f.wantAdmission(t, store.RunAdmissionPlanning)
	f.mustAccept(t, planOf("a"))
	f.wantAdmission(t, store.RunAdmissionAdmitted)
}

func (f dispatchRun) enqueue(tok store.ClaimToken, ordinal int64, id string) (string, error) {
	return f.s.EnqueueChildRun(context.Background(), tok, ordinal,
		store.Trigger{ID: id, Pipeline: "child", ParentRunID: "forged", ParentNodeID: "forged"}, time.Now())
}

// A child is keyed by its invocation: a retry of the same call after a lost
// response gets the same child and starts none, two calls get two children,
// and a later attempt reuses a child that has not failed.
func TestEnqueueChildRun_IsIdempotentPerInvocation(t *testing.T) {
	f := newDispatchRun(t, "run-children")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":2,"retry_auto":true}`))
	ctx := context.Background()
	tok := f.claim(t, "a", store.ClaimTokenWork)
	first, err := f.enqueue(tok, 0, "child-1")
	if err != nil || first != "child-1" {
		t.Fatalf("first call: %q %v", first, err)
	}
	if got, err := f.enqueue(tok, 0, "child-1-retry"); err != nil || got != "child-1" {
		t.Fatalf("retried call: %q %v, want child-1", got, err)
	}
	if _, err := f.s.GetTrigger(ctx, "child-1-retry"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the retried call started a second child: %v", err)
	}
	if got, err := f.enqueue(tok, 1, "child-2"); err != nil || got != "child-2" {
		t.Fatalf("second identical call: %q %v, want a child of its own", got, err)
	}
	child, err := f.s.GetTrigger(ctx, "child-1")
	if err != nil || child.ParentRunID != f.run || child.ParentNodeID != "a" {
		t.Fatalf("child parent = %+v (%v), want the claim's run and node", child, err)
	}

	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.enqueue(tok, 2, "child-late"); !errors.Is(err, store.ErrClaimNotLive) {
		t.Fatalf("enqueue after the claim ended: err = %v", err)
	}
	second := f.claim(t, "a", store.ClaimTokenWork)
	if got, err := f.enqueue(second, 0, "child-1-again"); err != nil || got != "child-1" {
		t.Fatalf("next attempt, unfinished child: %q %v, want child-1 reused", got, err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE triggers SET status = 'failed' WHERE id = ?`), "child-2"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.enqueue(second, 1, "child-2-again"); err != nil || got != "child-2-again" {
		t.Fatalf("next attempt, failed child: %q %v, want a new child", got, err)
	}

	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.enqueue(second, 3, "child-cancelled"); !errors.Is(err, store.ErrClaimCancelRequested) {
		t.Fatalf("enqueue after cancel: err = %v", err)
	}
}

func (f dispatchRun) expireClaim(t *testing.T, nodeID string) {
	t.Helper()
	if _, err := f.s.DB().ExecContext(context.Background(), storetest.Rebind(f.s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), f.run, nodeID); err != nil {
		t.Fatal(err)
	}
}

func recoverClaims(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := teststore.ReapUnits(context.Background(), s); err != nil {
		t.Fatalf("recover expired claims: %v", err)
	}
}

// A cancelled run whose last claimed pod vanishes still finishes: the lapsed
// claim ends the node cancelled and the run cancelled, and nothing can claim
// the node again.
func TestExpiry_ACancelledRunWhosePodVanishesFinishesCancelled(t *testing.T) {
	f := newDispatchRun(t, "run-expire-cancel")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":3,"retry_auto":true}`, "b:a"))
	f.claim(t, "a", store.ClaimTokenWork)
	ctx := context.Background()
	if err := f.s.RequestCancel(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	f.expireClaim(t, "a")
	recoverClaims(t, f.s)
	f.wantOutcome(t, "a", "cancelled")
	f.wantOutcome(t, "b", "cancelled")
	f.wantRun(t, "cancelled")
	if _, err := f.s.ClaimNamedNode(ctx, f.claimant, f.run, "a", "holder-late", time.Minute, store.NamedClaimOptions{}); err == nil {
		t.Fatal("a node of a cancelled run was claimed again")
	}
}

// No path awards a node of a run with a cancel request, even one settle has
// not yet reached.
func TestAward_RefusesARunWithACancelRequest(t *testing.T) {
	f := newDispatchRun(t, "run-award-cancel")
	f.mustAccept(t, planOf("a", "b"))
	ctx := context.Background()
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE runs SET cancel_requested_at = ? WHERE id = ?`), time.Now().UnixNano(), f.run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ClaimNamedNode(ctx, f.claimant, f.run, "a", "holder-a", time.Minute, store.NamedClaimOptions{}); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("award on a cancel-requested run: err = %v, want refused", err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE runs SET cancel_requested_at = NULL WHERE id = ?`), f.run); err != nil {
		t.Fatal(err)
	}
	f.claim(t, "b", store.ClaimTokenWork)
}

// A later attempt reuses a child only on evidence it is live or succeeded,
// and only for the same pipeline and arguments.
func TestEnqueueChildRun_ReusesOnlyALiveChildOfTheSameRequest(t *testing.T) {
	f := newDispatchRun(t, "run-child-reuse")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":3,"retry_auto":true}`))
	ctx := context.Background()
	enqueue := func(tok store.ClaimToken, ordinal int64, id, arg string) (string, error) {
		return f.s.EnqueueChildRun(ctx, tok, ordinal,
			store.Trigger{ID: id, Pipeline: "child", Args: map[string]string{"env": arg}}, time.Now())
	}
	tok := f.claim(t, "a", store.ClaimTokenWork)
	for i, id := range []string{"done-no-run", "cancelled-early", "succeeded", "other-args"} {
		if got, err := enqueue(tok, int64(i), id, "prod"); err != nil || got != id {
			t.Fatalf("enqueue %s: %q %v", id, got, err)
		}
	}
	if _, err := enqueue(tok, 3, "other-args-2", "staging"); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("same invocation with other arguments: err = %v, want conflict", err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE triggers SET status = 'done' WHERE id = ?`), "done-no-run"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RequestCancel(ctx, "cancelled-early"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CreateRun(ctx, store.Run{ID: "succeeded", Pipeline: "child", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	next := f.claim(t, "a", store.ClaimTokenWork)
	for i, want := range []string{"done-no-run-2", "cancelled-early-2", "succeeded", "other-args-2"} {
		arg := "prod"
		if i == 3 {
			arg = "staging"
		}
		if got, err := enqueue(next, int64(i), want, arg); err != nil || got != want {
			t.Fatalf("ordinal %d on the next attempt: %q %v, want %s", i, got, err, want)
		}
	}
}

// Only a planning claim the reaper found lost is planned again; a planning
// pod that reports its own failure as a lost agent fails the run.
func TestReportAttempt_APlanClaimCannotClaimItsOwnLoss(t *testing.T) {
	f := newDispatchRun(t, "run-plan-lost")
	tok := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	if _, err := f.report(tok, store.AttemptReport{Outcome: "failed", FailureReason: store.FailureAgentLost}); err != nil {
		t.Fatal(err)
	}
	f.wantOutcome(t, store.PlanNodeID, "failed")
	f.wantRun(t, "failed")

	g := newDispatchRun(t, "run-plan-reaped")
	g.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	g.expireClaim(t, store.PlanNodeID)
	recoverClaims(t, g.s)
	if n := g.node(t, store.PlanNodeID); n.Status != "pending" || n.ClaimedBy != "" {
		t.Fatalf("a reaped planning claim left plan %s claimed by %q, want requeued", n.Status, n.ClaimedBy)
	}
}
