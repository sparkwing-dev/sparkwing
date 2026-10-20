package store

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestReconcileMaterializeOnlyMissDoesNotAwardOrRelease(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "ready"}[ready], func(t *testing.T) {
			tenant, _, actor := reconcileFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,started_at=NULL,ready_at=NULL`)
			if ready {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET ready_at=1`)
			}
			before, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: before.Ref, Actor: actor, Policy: SourceMemo, MaterializeOnly: true})
			if err != nil || got.Kind != Dispatch || got.Claim != nil || len(got.Bearer) != 0 || !reflect.DeepEqual(got.Node, before) {
				t.Fatalf("probe changed execution authority: %+v, %v", got, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 0)
			rewriteSchemaCount(t, tenant.s.db, "capabilities", 2)
		})
	}
}

func TestReconcileMaterializeOnlyPreservesHolderAndTerminal(t *testing.T) {
	tenant, _, actor := reconcileFixture(t)
	request := ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: SourceNone, MaterializeOnly: true}
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != WaitHolder || got.Claim != nil || len(got.Bearer) != 0 {
		t.Fatalf("held probe: %+v, %v", got, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='done',outcome='failed',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,finished_at=1`)
	got, err = tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != KeepTerminal || got.Node.Outcome != NodeFailed {
		t.Fatalf("terminal probe: %+v, %v", got, err)
	}
}

func TestReconcileMaterializeOnlyHitCommitsSourceResult(t *testing.T) {
	tenant, request := materializationFixture(t, SourceManualRetry)
	request.MaterializeOnly = true
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != Materialize || got.Node.Outcome != NodeSuccess || got.Claim != nil || len(got.Bearer) != 0 {
		t.Fatalf("materialization: %+v, %v", got, err)
	}
}

func TestRunHostCancelPreservesHolderResultRights(t *testing.T) {
	tenant, _, actor := reconcileFixture(t)
	change := NodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, To: NodeCancelled, Cause: CauseCancel}
	first, err := tenant.TransitionNode(t.Context(), change)
	if err != nil || !first.CancelRequested {
		t.Fatalf("owner cancellation: %+v, %v", first, err)
	}
	second, err := tenant.TransitionNode(t.Context(), change)
	if err != nil || !second.Replayed || !second.CancelRequested || second.StateSeq != first.StateSeq {
		t.Fatalf("cancel replay: %+v, %v", second, err)
	}
	row, err := tenant.ReadNode(t.Context(), change.Ref)
	if err != nil || row.Status != StatusRunning || row.CapabilityPrefix != "claim" || row.CancelRequestedAt == nil {
		t.Fatalf("holder overwritten: %+v, %v", row, err)
	}
	change.Actor = Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", Holder: "runner", Generation: 2}
	change.Cause = CauseReport
	change.SourceAttempt = sourceAttempt(3)
	if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 10)
	if err != nil || len(events) != 2 || events[0].Kind != "job_cancel_requested" || events[1].Kind != "job_cancelled" {
		t.Fatalf("cancel events: %+v, %v", events, err)
	}
}
