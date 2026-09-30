package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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

func reapApprovals(t *testing.T, s *store.Store) [][2]string {
	t.Helper()
	pairs, err := store.Maintenance.ReapTimedOutApprovals(s, context.Background())
	if err != nil {
		t.Fatalf("reap approvals: %v", err)
	}
	return pairs
}

// A gate is decided once. A person resolving it and the reaper timing it out
// race under the run row; exactly one commits, the other writes nothing, and
// the gate's outcome and its dependent follow the winner.
func TestApproval_ResolveAndTimeoutHaveOneWinner(t *testing.T) {
	for i := range 6 {
		f := newDispatchRun(t, "run-gate-race-"+string(rune('a'+i)))
		f.mustAccept(t, planOf(`gate|"approval":{"timeout_ms":60000,"on_timeout":"approve"}`, "after:gate"))
		if n := f.node(t, "gate"); n.Status != store.NodeStatusApprovalPending {
			t.Fatalf("gate = %s, want approval_pending", n.Status)
		}
		f.expireGate(t, "gate")
		var wg sync.WaitGroup
		var resolveErr, reapErr error
		var reaped [][2]string
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, resolveErr = f.s.ResolveApproval(context.Background(), f.run, "gate", store.ApprovalResolutionDenied, "korey", "no")
		}()
		go func() {
			defer wg.Done()
			reaped, reapErr = store.Maintenance.ReapTimedOutApprovals(f.s, context.Background())
		}()
		wg.Wait()
		if reapErr != nil {
			t.Fatalf("reap: %v", reapErr)
		}
		humanWon := resolveErr == nil
		if !humanWon && !errors.Is(resolveErr, store.ErrLockHeld) {
			t.Fatalf("resolve: %v", resolveErr)
		}
		if reaperWon := len(reaped) == 1; reaperWon == humanWon {
			t.Fatalf("round %d: resolve err %v and reaper decided %v; exactly one must win", i, resolveErr, reaped)
		}
		a, err := f.s.GetApproval(context.Background(), f.run, "gate")
		if err != nil {
			t.Fatal(err)
		}
		if humanWon {
			if a.Resolution != store.ApprovalResolutionDenied {
				t.Fatalf("resolution = %s after the person won", a.Resolution)
			}
			f.wantOutcome(t, "gate", "failed")
			f.wantOutcome(t, "after", "cancelled")
			continue
		}
		if a.Resolution != store.ApprovalResolutionTimedOut {
			t.Fatalf("resolution = %s after the reaper won", a.Resolution)
		}
		f.wantOutcome(t, "gate", "success")
		f.wantReleased(t, "after", true)
	}
}

// The reaper decides a controller gate by its on_timeout policy, and never
// before its timeout; a gate with no timeout waits for a person.
func TestApproval_TimeoutHonoursOnTimeout(t *testing.T) {
	f := newDispatchRun(t, "run-gate-policy")
	f.mustAccept(t, planOf(
		`approve|"approval":{"timeout_ms":1000,"on_timeout":"approve"}`, "after-approve:approve",
		`deny|"approval":{"timeout_ms":1000,"on_timeout":"deny"}`, "after-deny:deny",
		`fail|"approval":{"timeout_ms":1000}`,
		`fresh|"approval":{"timeout_ms":3600000}`,
		`forever|"approval":{}`,
	))
	if got := reapApprovals(t, f.s); len(got) != 0 {
		t.Fatalf("reaped %v before any timeout passed", got)
	}
	for _, id := range []string{"approve", "deny", "fail", "forever"} {
		f.expireGate(t, id)
	}
	if got := reapApprovals(t, f.s); len(got) != 3 {
		t.Fatalf("reaped %v, want the three expired gates with a timeout", got)
	}
	f.wantOutcome(t, "approve", "success")
	f.wantReleased(t, "after-approve", true)
	f.wantOutcome(t, "deny", "failed")
	f.wantOutcome(t, "after-deny", "cancelled")
	f.wantOutcome(t, "fail", "failed")
	for _, id := range []string{"fresh", "forever"} {
		if n := f.node(t, id); n.Status != store.NodeStatusApprovalPending {
			t.Fatalf("%s = %s/%s, want still waiting", id, n.Status, n.Outcome)
		}
	}
	if _, err := f.s.ResolveApproval(context.Background(), f.run, "fresh", store.ApprovalResolutionApproved, "korey", ""); err != nil {
		t.Fatal(err)
	}
	f.wantOutcome(t, "fresh", "success")
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
	if _, err := store.Maintenance.RecoverExpiredNodeClaims(s, context.Background()); err != nil {
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

// A lapsed claim is a failed attempt: within the node's budget the node goes
// back to the queue at a new generation with its dependents held, and past it
// the node fails and settle cancels the dependents and fails the run.
func TestExpiry_ALapsedClaimRetriesThroughSettle(t *testing.T) {
	f := newDispatchRun(t, "run-expire-retry")
	f.mustAccept(t, planOf(`a|"modifiers":{"retry":1,"retry_auto":true}`, "b:a"))
	first := f.claim(t, "a", store.ClaimTokenWork)
	f.expireClaim(t, "a")
	if _, err := f.s.ReapExpiredNodeClaims(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := f.node(t, "a")
	if a.Status != "pending" || a.ClaimedBy != "" || a.AttemptsConsumed != 1 {
		t.Fatalf("a = %s claimed %q consumed %d, want requeued after one lost attempt", a.Status, a.ClaimedBy, a.AttemptsConsumed)
	}
	f.wantReleased(t, "b", false)
	if _, err := f.report(first, store.AttemptReport{Outcome: "success"}); !errors.Is(err, store.ErrClaimResultConflict) {
		t.Fatalf("a report from the lapsed claim: err = %v, want conflict", err)
	}
	second := f.claim(t, "a", store.ClaimTokenWork)
	if second.Generation <= first.Generation {
		t.Fatalf("requeued claim at generation %d, want above %d", second.Generation, first.Generation)
	}
	f.expireClaim(t, "a")
	recoverClaims(t, f.s)
	f.wantOutcome(t, "a", "failed")
	f.wantOutcome(t, "b", "cancelled")
	f.wantRun(t, "failed")
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

// The orchestrator's expiry pass releases its claims even when the
// controller-dispatched pass cannot settle a run.
func TestExpiry_ADispatchFailureDoesNotBlockTheOrchestratorPass(t *testing.T) {
	f := newDispatchRun(t, "run-expire-broken")
	f.mustAccept(t, planOf("a", "b:a"))
	f.claim(t, "a", store.ClaimTokenWork)
	ctx := context.Background()
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
		`UPDATE nodes SET deps_json = ? WHERE run_id = ? AND node_id = 'b'`), []byte("not json"), f.run); err != nil {
		t.Fatal(err)
	}
	f.expireClaim(t, "a")
	for name, reap := range map[string]func() error{
		"reap": func() error { _, err := f.s.ReapExpiredNodeClaims(ctx); return err },
		"recover": func() error {
			_, err := store.Maintenance.RecoverExpiredNodeClaims(f.s, ctx)
			return err
		},
	} {
		run := "legacy-" + name
		if err := f.s.CreateRun(ctx, store.Run{ID: run, Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := f.s.CreateNode(ctx, store.Node{RunID: run, NodeID: "n", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ClaimNamedNode(ctx, f.claimant, run, "n", "holder-n", time.Minute, store.NamedClaimOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s,
			`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ?`), time.Now().Add(-time.Second).UnixNano(), run); err != nil {
			t.Fatal(err)
		}
		if err := reap(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n, err := f.s.GetNode(ctx, run, "n")
		if err != nil || n.ClaimedBy != "" {
			t.Fatalf("%s left the orchestrator claim held by %q (%v)", name, n.ClaimedBy, err)
		}
	}
	if n := f.node(t, "a"); n.ClaimedBy == "" {
		t.Fatal("the broken run settled; the test no longer exercises a dispatch failure")
	}
}
