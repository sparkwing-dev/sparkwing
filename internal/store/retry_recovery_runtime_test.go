package store

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRecoveryPreservesTheAcceptedJobInItsTeam(t *testing.T) {
	tenant, now, _ := liveHolderFixture(t)
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_hash='accepted-spec',lease_expires_at=? WHERE team='a' AND run_id='run'`, now.Add(-time.Second).UnixNano())
	before, err := tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), SingletonLease{Name: "controller.reaper", Holder: "replica", Generation: 1}, 10)
	if err != nil || len(reaped) != 1 || reaped[0].Team != tenant.team || runspec.RunID(reaped[0].RunID) != ref.RunID {
		t.Fatalf("recovery identity = %+v, %v", reaped, err)
	}
	after, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || !bytes.Equal(before.Spec, after.Spec) || before.SpecHash != after.SpecHash || !reflect.DeepEqual(before.Execution, after.Execution) {
		t.Fatalf("recovery changed accepted job: before=%+v, after=%+v, error=%v", before, after, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "runs", 1)
	rewriteSchemaCount(t, tenant.s.db, "nodes", 1)
	if _, err := (&Tenant{s: tenant.s, team: "b"}).ReadNode(t.Context(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign recovery read = %v", err)
	}
}

func TestReapedClaimCannotReportAfterReclaim(t *testing.T) {
	tenant, now, oldHolder := liveHolderFixture(t)
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run'`, now.Add(-time.Second).UnixNano())
	lease := SingletonLease{Name: "controller.reaper", Holder: "replica", Generation: 1}
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Requeued || reaped[0].Generation != 2 {
		t.Fatalf("reaped = %+v, %v", reaped, err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Generation != 2 || row.Attempt != 3 || row.Disruptions != 1 {
		t.Fatalf("requeued row = %+v, %v", row, err)
	}
	oldReport := nodeTransition{Ref: ref, To: NodeSuccess, Actor: oldHolder, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeSuccess}}}
	if _, err := tenant.TransitionNode(t.Context(), oldReport); err == nil {
		t.Fatal("reaped holder reported into pending work")
	}

	expiry := now.Add(time.Hour)
	claim := authorityRoot("next-claim", "a")
	claim.Grants = []capability.Grant{capability.NodeReport, capability.NodeResult}
	claim.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "next-runner", Generation: 3}
	claim.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, claim)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='running',claimed_by='next-runner',capability_prefix='next-claim',generation=3,attempt=4,started_at=?,lease_expires_at=? WHERE team='a' AND run_id='run'`, now.UnixNano(), expiry.UnixNano())
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',4,3,'next-runner','next-claim','runner','next-runner','remote',?)`, now.UnixNano())
	if _, err := tenant.TransitionNode(t.Context(), oldReport); err == nil {
		t.Fatal("reaped holder reported into a later generation")
	}
	oldReport.SourceAttempt = sourceAttempt(4)
	oldReport.Actor = Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Prefix}, CapabilityPrefix: claim.Prefix, NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "next-runner", Generation: 3}}
	if _, err := tenant.TransitionNode(t.Context(), oldReport); err != nil {
		t.Fatal(err)
	}
	var outcome, reason string
	if err := tenant.s.db.QueryRow(`SELECT outcome,failure_reason FROM node_attempts WHERE team='a' AND run_id='run' AND attempt=3`).Scan(&outcome, &reason); err != nil || outcome != string(NodeFailed) || reason != FailureAgentLost {
		t.Fatalf("old attempt = %s/%s, %v", outcome, reason, err)
	}
}

func TestRecoveryNeverResetsTerminalJobs(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "disruption_limit", true: "cancel_requested"}[cancel], func(t *testing.T) {
			tenant, now, reaper := stateFixture(t)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET disruptions=2 WHERE team='a' AND run_id='run'`)
			if cancel {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=? WHERE team='a' AND id='run'`, now.UnixNano())
			}
			reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), SingletonLease{Name: "controller.reaper", Holder: "replica", Generation: 1}, 10)
			if err != nil || len(reaped) != 1 || reaped[0].Requeued {
				t.Fatalf("reaped = %+v, %v", reaped, err)
			}
			ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
			before, err := tenant.ReadNode(t.Context(), ref)
			want := NodeFailed
			if cancel {
				want = NodeCancelled
			}
			if err != nil || before.Status != StatusDone || before.Outcome != want {
				t.Fatalf("terminal = %+v, %v", before, err)
			}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: ref, To: nodePending, Actor: reaper, Cause: CauseLeaseLapse}); err == nil {
				t.Fatal("terminal job reopened")
			}
			after, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || after.StateSeq != before.StateSeq || after.Generation != before.Generation || after.Attempt != before.Attempt {
				t.Fatalf("refused reset changed terminal job: %+v, %v", after, err)
			}
		})
	}
}
