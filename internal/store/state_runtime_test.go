package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func stateFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	s := &Store{db: rewriteSchemaDB(t), dialect: DialectSQLite, clock: func() time.Time { return now }}
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, []byte(`{"version":1,"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`), []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,started_at,lease_expires_at,expansion) VALUES ('a','run','job','running',?,?,'runner','claim',2,3,?,?,'expected')`, []byte(`{"id":"job"}`), []byte(`{}`), now.Add(-time.Minute).UnixNano(), now.Add(-time.Second).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',3,2,'runner','claim','runner','runner','remote',?)`, now.Add(-time.Minute).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.reaper','replica',?,1)`, now.Add(time.Minute).UnixNano())
	actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.reaper", EvalFence: runspec.EvalFence{Holder: "replica", Generation: 1}}}
	return &Tenant{s: s, team: "a"}, &now, actor
}

func TestTransitionLapseRequeuesInSameRun(t *testing.T) {
	tenant, _, actor := stateFixture(t)
	ctx := context.Background()
	change := nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: CauseLeaseLapse}
	applied, err := tenant.TransitionNode(ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	if applied.StateSeq != 1 || applied.Generation != 2 || applied.Attempt != 3 {
		t.Fatalf("applied = %+v", applied)
	}
	row, err := tenant.ReadNode(ctx, change.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusPending || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.ReadyAt != nil || row.Disruptions != 1 || row.ApplicationRetries != 0 {
		t.Fatalf("row = %+v", row)
	}
	replay, err := tenant.TransitionNode(ctx, change)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	events, err := tenant.ReadStateEvents(ctx, "run", 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != "job_requeued" {
		t.Fatalf("events = %+v, %v", events, err)
	}
}

func TestTransitionCancelWinsOverLoss(t *testing.T) {
	tenant, now, actor := stateFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET cancel_requested_at=? WHERE team='a' AND run_id='run'`, now.UnixNano())
	ctx := context.Background()
	if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: CauseLeaseLapse}); err == nil {
		t.Fatal("cancelled work was requeued")
	}
	change := nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: NodeCancelled, Actor: actor, Cause: CauseLeaseLapse}
	if _, err := tenant.TransitionNode(ctx, change); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(ctx, change.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if row.Outcome != NodeCancelled || row.Expansion != ExpansionNone || row.ClaimedBy != "" {
		t.Fatalf("row = %+v", row)
	}
	change.To = NodeSuccess
	var illegal *ErrIllegalTransition
	if _, err := tenant.TransitionNode(ctx, change); !errors.As(err, &illegal) || illegal.Reason != ReasonTerminal {
		t.Fatalf("terminal mutation = %v", err)
	}
}

func TestTransitionTenantAndExpiredReaperFence(t *testing.T) {
	tenant, now, actor := stateFixture(t)
	ctx := context.Background()
	other := &Tenant{s: tenant.s, team: "b"}
	if _, err := other.ReadNode(ctx, controllerwire.NodeRef{RunID: "run", NodeID: "job"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-team read = %v", err)
	}
	*now = now.Add(time.Minute)
	if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: CauseLeaseLapse}); err == nil {
		t.Fatal("expired reaper changed work")
	}
	row, err := tenant.ReadNode(ctx, controllerwire.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.StateSeq != 0 {
		t.Fatalf("refused write changed row: %+v, %v", row, err)
	}
}

func TestRunCancellationDrainsBeforeTerminal(t *testing.T) {
	tenant, _, actor := stateFixture(t)
	ctx := context.Background()
	request := RunTransition{RunID: "run", To: RunCancelled, Actor: actor, Cause: RunCancel}
	result, err := tenant.TransitionRun(ctx, request)
	if err != nil || !result.CancelRequested {
		t.Fatalf("cancel = %+v, %v", result, err)
	}
	run, err := tenant.ReadRun(ctx, "run")
	if err != nil || run.State != RunRunning || run.CancelRequestedAt == nil {
		t.Fatalf("run = %+v, %v", run, err)
	}
	finish := RunTransition{RunID: "run", To: RunCancelled, Actor: actor, Cause: RunFinish}
	if _, err = tenant.TransitionRun(ctx, finish); err == nil {
		t.Fatal("run finished with undrained job")
	}
	if _, err = tenant.TransitionNode(ctx, nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: NodeCancelled, Actor: actor, Cause: CauseLeaseLapse}); err != nil {
		t.Fatal(err)
	}
	if _, err = tenant.TransitionRun(ctx, finish); err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(ctx, "run")
	if err != nil || run.State != RunCancelled || run.FinishedAt == nil {
		t.Fatalf("finished run = %+v, %v", run, err)
	}
}

func TestRunOwnerLossPreservesCancellation(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE team='a' AND id='run'`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','run','claimed','trigger',1,1,'owner',1,?,?,?)`, now.Add(-time.Minute).UnixNano(), []byte(`[]`), []byte(`[]`))
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
	if _, err := tenant.TransitionRun(context.Background(), RunTransition{RunID: "run", To: RunFailed, Actor: actor, Cause: RunOwnerLost}); err == nil {
		t.Fatal("owner loss overrode requested cancellation with failure")
	}
}

func TestRunQueueTimeoutPreservesLiveOwner(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, RunPending)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json = ? WHERE team='a' AND id='run'`, []byte(`{"version":1,"admitted_at":"2027-01-01T00:00:00Z","request":{"options":{"claim_wait":{"set":true,"value":1}}},"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','run','claimed','trigger',1,1,'owner',1,?,?,?)`, now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorReaper, Owner: OwnerLeaseRef{Kind: OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
	if _, err := tenant.TransitionRun(context.Background(), RunTransition{RunID: "run", To: RunFailed, Actor: actor, Cause: RunQueueTimeout}); err == nil {
		t.Fatal("queue timeout failed a live claimed owner")
	}
}

func liveHolderFixture(t *testing.T) (*Tenant, *time.Time, Actor) {
	tenant, now, _ := stateFixture(t)
	expiry := now.Add(time.Hour)
	c := authorityRoot("claim", "a")
	c.Grants = []capability.Grant{capability.NodeReport, capability.NodeResult, capability.LogTail}
	c.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Holder: "runner"}
	c.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, c)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=?,execution_json=?,expansion='' WHERE team='a' AND run_id='run'`, expiry.UnixNano(), []byte(`{"retry":{"set":true,"value":1}}`))
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}}
	return tenant, now, actor
}

func TestTransitionApplicationRetryKeepsClaimAndOrdinal(t *testing.T) {
	tenant, _, actor := liveHolderFixture(t)
	ctx := context.Background()
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	applied, err := tenant.TransitionNode(ctx, nodeTransition{Ref: ref, To: NodeRunning, Actor: actor, Cause: CauseRetry, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "verify"}}})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Attempt != 4 || applied.Generation != 2 {
		t.Fatalf("retry = %+v", applied)
	}
	row, err := tenant.ReadNode(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if row.ClaimedBy != "runner" || row.ApplicationRetries != 1 || row.Disruptions != 0 {
		t.Fatalf("retry row = %+v", row)
	}
	result := nodeTransition{Ref: ref, To: NodeSuccess, Actor: actor, Cause: CauseReport, SourceAttempt: sourceAttempt(4), Extra: transitionExtra{Result: &NodeResult{Outcome: NodeSuccess}}}
	if _, err = tenant.TransitionNode(ctx, result); err != nil {
		t.Fatal(err)
	}
	replay, err := tenant.TransitionNode(ctx, result)
	if err != nil || !replay.Replayed {
		t.Fatalf("result replay = %+v, %v", replay, err)
	}
	events, err := tenant.ReadStateEvents(ctx, "run", 0, 10)
	if err != nil || len(events) != 2 || events[0].Kind != "job_retrying" || events[1].Kind != "job_succeeded" {
		t.Fatalf("events = %+v, %v", events, err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1,revocation_generation=revocation_generation+1 WHERE prefix='claim'`)
	if _, err = tenant.TransitionNode(ctx, result); err == nil {
		t.Fatal("revoked authority replayed result")
	}
}

func TestTransitionHolderRights(t *testing.T) {
	tenant, now, holder := liveHolderFixture(t)
	expiry := now.Add(time.Hour)
	owner := authorityRoot("owner", "a")
	owner.Grants = []capability.Grant{capability.TriggerHost}
	owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "run", Holder: "host", Generation: 1}
	owner.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, owner)
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','run','claimed','trigger',1,1,'host','owner',1,?,?,?)`, expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, CapabilityPrefix: "owner", Owner: OwnerLeaseRef{Kind: OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: "host", Generation: 1}}}
	change := nodeTransition{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, To: NodeFailed, Actor: actor, Cause: CauseReport}
	if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("owner finished another holder's job")
	}
	holder.Generation++
	change.Actor = holder
	change.SourceAttempt = sourceAttempt(3)
	if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("wrong generation finished job")
	}
	row, err := tenant.ReadNode(t.Context(), change.Ref)
	if err != nil || row.Status != StatusRunning || row.StateSeq != 0 {
		t.Fatalf("refused writes changed row: %+v, %v", row, err)
	}
}

func TestHolderFenceCannotBeReboundToAnotherTarget(t *testing.T) {
	for _, target := range []string{"run", "job"} {
		t.Run(target, func(t *testing.T) {
			tenant, _, actor := liveHolderFixture(t)
			ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if target == "run" {
				actor.RunID = "other-run"
			} else {
				actor.NodeID = "other-job"
			}
			_, err = tenant.TransitionNode(t.Context(), ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(3), Outcome: NodeSuccess, Result: &NodeResult{Outcome: NodeSuccess}})
			if err == nil {
				t.Fatal("holder fence was rebound to the command target")
			}
			after, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || after.StateSeq != before.StateSeq || after.Status != before.Status {
				t.Fatalf("refused fence changed state: %+v, %v", after, err)
			}
		})
	}
}
