package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func approvalReaperFixture(t *testing.T, policy string) (*Tenant, runspec.NodeRef, *time.Time, nodeTransition) {
	t.Helper()
	timeout := int64(1000)
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "gate", Approval: &nodeproto.ApprovalSpec{TimeoutMS: &timeout, OnTimeout: policy}},
		{ID: "after", Needs: []string{"gate"}},
	}}
	tenant, request, now := planFixture(t, runspec.DispatchController)
	raw, err := json.Marshal(testBodyPlan(plan))
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Plan = raw
	execution, err := settings.ResolvePlan(settings.PlanInputs{Declarations: runspec.CheckoutDeclarations{Version: 1}, Request: request.Seal.Request, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	request.Seal.Resolved.Settings = execution
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	approver := authorityRoot("approver", "a")
	approver.Grants = []capability.Grant{capability.Grant("approvals.write")}
	authorityPersist(t, tenant.s, approver)
	ref := runspec.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "gate"}
	resolve := nodeTransition{
		Ref: ref, To: runstate.NodeFailed, Cause: causeApprovalResolve,
		Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: approver.Prefix}},
		Extra: transitionExtra{Approval: &runstate.ApprovalResolution{Decision: "denied", Comment: "declined"}},
	}
	return tenant, ref, now, resolve
}

func TestApprovalReaperUsesDeadlineAndPolicyOnce(t *testing.T) {
	for _, policy := range []string{"approve", "deny", "fail", ""} {
		t.Run(policy, func(t *testing.T) {
			tenant, ref, now, _ := approvalReaperFixture(t, policy)
			*now = now.Add(time.Second - time.Nanosecond)
			if got, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(got) != 0 {
				t.Fatalf("before deadline: %+v, %v", got, err)
			}
			*now = now.Add(time.Nanosecond)
			for pass := range 2 {
				got, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10)
				if err != nil || len(got) != 1-pass {
					t.Fatalf("pass %d: %+v, %v", pass, got, err)
				}
			}
			gate, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || gate.StateSeq != 2 || gate.FinishedAt == nil {
				t.Fatalf("gate: %+v, %v", gate, err)
			}
			after, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: ref.RunID, NodeID: "after"})
			if err != nil {
				t.Fatal(err)
			}
			if policy == "approve" {
				if gate.Outcome != runstate.NodeSuccess || after.ReadyAt == nil || after.Status != runstate.StatusPending {
					t.Fatalf("approved gate/dependent: %+v, %+v", gate, after)
				}
			} else if gate.Outcome != runstate.NodeFailed || after.Outcome != runstate.NodeCancelled {
				t.Fatalf("failed gate/dependent: %+v, %+v", gate, after)
			}
			var resolved int64
			var resolution, approver string
			if err := tenant.s.db.QueryRow(`SELECT resolved_at,resolution,approver FROM approvals WHERE team=? AND run_id=? AND node_id=?`, tenant.team, ref.RunID, ref.NodeID).Scan(&resolved, &resolution, &approver); err != nil || resolved != now.UnixNano() || resolution != "timed_out" || approver != "" {
				t.Fatalf("resolution: %d %s by %q, %v", resolved, resolution, approver, err)
			}
		})
	}
}

func TestApprovalReaperLeavesIndefiniteAndResolvedGates(t *testing.T) {
	for _, mode := range []string{"indefinite", "resolved"} {
		t.Run(mode, func(t *testing.T) {
			tenant, ref, now, resolve := approvalReaperFixture(t, "approve")
			if mode == "indefinite" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE approvals SET timeout_ms=0`)
			} else if _, err := tenant.TransitionNode(t.Context(), resolve); err != nil {
				t.Fatal(err)
			}
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(time.Minute)
			if got, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(got) != 0 {
				t.Fatalf("reaped %s gate: %+v, %v", mode, got, err)
			}
			after, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || after.Status != before.Status || after.Outcome != before.Outcome || after.StateSeq != before.StateSeq {
				t.Fatalf("gate changed: %+v, %v", after, err)
			}
		})
	}
}

func TestApprovalReaperIgnoresForgedAuthority(t *testing.T) {
	tenant, ref, now, _ := approvalReaperFixture(t, "approve")
	*now = now.Add(time.Second)
	actor := runstate.Actor{Kind: runstate.ActorReaper, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "forged"}, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, To: runstate.NodeSuccess, Cause: causeApprovalTimeout, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	var approver string
	if err := tenant.s.db.QueryRow(`SELECT approver FROM approvals WHERE team=? AND run_id=? AND node_id=?`, tenant.team, ref.RunID, ref.NodeID).Scan(&approver); err != nil || approver != "" {
		t.Fatalf("resolver identity %q, %v", approver, err)
	}
}

func TestApprovalResolutionAndReaperHaveOneWinner(t *testing.T) {
	for range 6 {
		tenant, ref, now, resolve := approvalReaperFixture(t, "approve")
		*now = now.Add(time.Second)
		var wg sync.WaitGroup
		start := make(chan struct{})
		var resolveErr, reapErr error
		var reaped []ReapedUnit
		wg.Go(func() { <-start; _, resolveErr = tenant.TransitionNode(t.Context(), resolve) })
		wg.Go(func() { <-start; reaped, reapErr = tenant.s.AsOperator().ReapUnits(t.Context(), 10) })
		close(start)
		wg.Wait()
		if reapErr != nil {
			t.Fatal(reapErr)
		}
		humanWon := resolveErr == nil
		var illegal *ErrIllegalTransition
		if !humanWon && (!errors.As(resolveErr, &illegal) || illegal.Reason != ReasonTerminal) {
			t.Fatalf("resolution: %v", resolveErr)
		}
		if (len(reaped) == 1) == humanWon {
			t.Fatalf("resolution %v, reaper %+v: expected one winner", resolveErr, reaped)
		}
		gate, err := tenant.ReadNode(t.Context(), ref)
		if err != nil || gate.StateSeq != 2 {
			t.Fatalf("gate: %+v, %v", gate, err)
		}
		want := runstate.NodeSuccess
		if humanWon {
			want = runstate.NodeFailed
		}
		if gate.Outcome != want {
			t.Fatalf("outcome %s, expected %s", gate.Outcome, want)
		}
	}
}

func TestApprovalReaperRollsBackResolutionWithLifecycle(t *testing.T) {
	for _, table := range []string{"approvals", "nodes", "events", "run_effects"} {
		t.Run(table, func(t *testing.T) {
			tenant, ref, now, _ := approvalReaperFixture(t, "deny")
			before, err := tenant.ReadStateEvents(t.Context(), string(ref.RunID), 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			verb := "UPDATE"
			if table == "events" || table == "run_effects" {
				verb = "INSERT"
			}
			rewriteSchemaExec(t, tenant.s.db, `CREATE TRIGGER refuse_timeout BEFORE `+verb+` ON `+table+` BEGIN SELECT RAISE(ABORT,'persistence unavailable'); END`)
			*now = now.Add(time.Second)
			if got, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err == nil || len(got) != 0 {
				t.Fatalf("failed persistence: %+v, %v", got, err)
			}
			gate, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || gate.Status != runstate.StatusApprovalPending || gate.StateSeq != 1 || gate.FinishedAt != nil {
				t.Fatalf("partial gate: %+v, %v", gate, err)
			}
			var resolved sql.NullInt64
			if err := tenant.s.db.QueryRow(`SELECT resolved_at FROM approvals WHERE team=? AND run_id=? AND node_id=?`, tenant.team, ref.RunID, ref.NodeID).Scan(&resolved); err != nil || resolved.Valid {
				t.Fatalf("partial resolution: %+v, %v", resolved, err)
			}
			after, err := tenant.ReadStateEvents(t.Context(), string(ref.RunID), 0, 100)
			if err != nil || len(after) != len(before) {
				t.Fatalf("partial events: %+v, %v", after, err)
			}
			rewriteSchemaExec(t, tenant.s.db, `DROP TRIGGER refuse_timeout`)
			if got, err := tenant.s.AsOperator().ReapUnits(t.Context(), 10); err != nil || len(got) != 1 {
				t.Fatalf("retry: %+v, %v", got, err)
			}
		})
	}
}

func TestCancelledApprovalLeavesPendingListAndKeepsHistory(t *testing.T) {
	tenant, ref, _, _ := approvalReaperFixture(t, "deny")
	actor := authorityMember(t, tenant.s, "operator", RoleOwner)
	actor.Kind = runstate.ActorOperator
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: ref.RunID, To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: actor, Reason: "user_cancelled"}); err != nil {
		t.Fatal(err)
	}
	pending, err := tenant.ListPendingApprovals(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending approvals after cancellation: %+v, %v", pending, err)
	}
	gate, err := tenant.GetApproval(t.Context(), string(ref.RunID), string(ref.NodeID))
	if err != nil || gate.NodeID != string(ref.NodeID) {
		t.Fatalf("cancelled approval history: %+v, %v", gate, err)
	}
}
