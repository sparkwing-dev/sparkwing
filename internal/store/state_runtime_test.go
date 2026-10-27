package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func stateFixture(t *testing.T) (*Tenant, *time.Time, runstate.Actor) {
	t.Helper()
	now := time.Unix(1700000000, 0).UTC()
	s := &Store{db: rewriteSchemaDB(t), dialect: DialectSQLite, clock: func() time.Time { return now }}
	rewriteSchemaExec(t, s.db, `INSERT INTO teams(name,created_at,updated_at) VALUES ('a',1,1),('b',1,1)`)
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('a','run',1,'p','running',?,'request',1,'gh:1','host','execute','account',1,'r30',30)`, []byte(`{"version":1,"request":{"git":{"repository":{"key":"gh:1"}}},"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,claimed_by,capability_prefix,generation,attempt,started_at,lease_expires_at,expansion) VALUES ('a','run','job','running',?,?,'runner','claim',2,3,?,?,'expected')`, []byte(`{"id":"job"}`), []byte(`{}`), now.Add(-time.Minute).UnixNano(), now.Add(-time.Second).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',3,2,'runner','claim','runner','runner','remote',?)`, now.Add(-time.Minute).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.reaper','replica',?,1)`, now.Add(time.Minute).UnixNano())
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, Name: "controller.reaper", EvalFence: runspec.EvalFence{Holder: "replica", Generation: 1}}}
	return &Tenant{s: s, team: "a"}, &now, actor
}

func TestTransitionLapseRequeuesInSameRun(t *testing.T) {
	tenant, _, actor := stateFixture(t)
	ctx := context.Background()
	change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: runstate.CauseLeaseLapse}
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
	if row.Status != runstate.StatusPending || row.ClaimedBy != "" || row.LeaseExpiresAt != nil || row.ReadyAt != nil || row.Disruptions != 1 || row.ApplicationRetries != 0 {
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
	if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: runstate.CauseLeaseLapse}); err == nil {
		t.Fatal("cancelled work was requeued")
	}
	change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeCancelled, Actor: actor, Cause: runstate.CauseLeaseLapse}
	if _, err := tenant.TransitionNode(ctx, change); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(ctx, change.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if row.Outcome != runstate.NodeCancelled || row.Expansion != runstate.ExpansionNone || row.ClaimedBy != "" {
		t.Fatalf("row = %+v", row)
	}
	change.To = runstate.NodeSuccess
	var illegal *ErrIllegalTransition
	if _, err := tenant.TransitionNode(ctx, change); !errors.As(err, &illegal) || illegal.Reason != ReasonTerminal {
		t.Fatalf("terminal mutation = %v", err)
	}
}

func TestTransitionTenantAndExpiredReaperFence(t *testing.T) {
	tenant, now, actor := stateFixture(t)
	ctx := context.Background()
	other := &Tenant{s: tenant.s, team: "b"}
	if _, err := other.ReadNode(ctx, runspec.NodeRef{RunID: "run", NodeID: "job"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-team read = %v", err)
	}
	*now = now.Add(time.Minute)
	if _, err := tenant.TransitionNode(ctx, nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Actor: actor, Cause: runstate.CauseLeaseLapse}); err == nil {
		t.Fatal("expired reaper changed work")
	}
	row, err := tenant.ReadNode(ctx, runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || row.StateSeq != 0 {
		t.Fatalf("refused write changed row: %+v, %v", row, err)
	}
}

func TestRunCancellationDrainsBeforeTerminal(t *testing.T) {
	tenant, _, actor := stateFixture(t)
	ctx := context.Background()
	request := runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Actor: actor, Cause: runstate.RunCancel}
	result, err := tenant.TransitionRun(ctx, request)
	if err != nil || !result.CancelRequested {
		t.Fatalf("cancel = %+v, %v", result, err)
	}
	run, err := tenant.ReadRun(ctx, "run")
	if err != nil || run.State != runstate.RunRunning || run.CancelRequestedAt == nil {
		t.Fatalf("run = %+v, %v", run, err)
	}
	finish := runstate.RunTransition{RunID: "run", To: runstate.RunCancelled, Actor: actor, Cause: runstate.RunFinish}
	if _, err = tenant.TransitionRun(ctx, finish); err == nil {
		t.Fatal("run finished with undrained job")
	}
	if _, err = tenant.TransitionNode(ctx, nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeCancelled, Actor: actor, Cause: runstate.CauseLeaseLapse}); err != nil {
		t.Fatal(err)
	}
	if _, err = tenant.TransitionRun(ctx, finish); err != nil {
		t.Fatal(err)
	}
	run, err = tenant.ReadRun(ctx, "run")
	if err != nil || run.State != runstate.RunCancelled || run.FinishedAt == nil {
		t.Fatalf("finished run = %+v, %v", run, err)
	}
}

func TestRunOwnerLossPreservesCancellation(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, runstate.RunRunning)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=? WHERE team='a' AND id='run'`, now.UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at,capability_prefix) VALUES ('a','run','claimed','trigger',1,1,'owner',1,?,?,?,1,'claim')`, now.Add(-time.Minute).UnixNano(), []byte(`[]`), []byte(`[]`))
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
	if _, err := tenant.TransitionRun(context.Background(), runstate.RunTransition{RunID: "run", To: runstate.RunFailed, Actor: actor, Cause: runstate.RunOwnerLost}); err == nil {
		t.Fatal("owner loss overrode requested cancellation with failure")
	}
}

func TestRunQueueTimeoutPreservesLiveOwner(t *testing.T) {
	s, tenant, now := maintenanceStore(t)
	maintenanceRun(t, s, "run", 1, runstate.RunPending)
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json = ? WHERE team='a' AND id='run'`, []byte(`{"version":1,"admitted_at":"2027-01-01T00:00:00Z","request":{"options":{"claim_wait":{"set":true,"value":1}}},"provenance":{"trust":"asserted","pipeline":"p","writer":"account/fixture"}}`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at,capability_prefix) VALUES ('a','run','claimed','trigger',1,1,'owner',1,?,?,?,1,'claim')`, now.Add(time.Hour).UnixNano(), []byte(`[]`), []byte(`[]`))
	lease, err := s.AsOperator().AcquireSingletonLease(context.Background(), "controller.reaper", "replica", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController, Name: lease.Name, EvalFence: runspec.EvalFence{Holder: lease.Holder, Generation: runspec.ClaimGeneration(lease.Generation)}}}
	if _, err := tenant.TransitionRun(context.Background(), runstate.RunTransition{RunID: "run", To: runstate.RunFailed, Actor: actor, Cause: runstate.RunQueueTimeout}); err == nil {
		t.Fatal("queue timeout failed a live claimed owner")
	}
}

func liveHolderFixture(t *testing.T) (*Tenant, *time.Time, runstate.Actor) {
	tenant, now, _ := stateFixture(t)
	expiry := now.Add(time.Hour)
	c := authorityRoot("claim", "a")
	c.Grants = []capability.Grant{capability.NodeReport, capability.NodeResult, capability.LogTail}
	c.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Generation: 2, Holder: "runner"}
	c.ExpiresAt = &expiry
	authorityPersist(t, tenant.s, c)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET lease_expires_at=?,execution_json=?,expansion='' WHERE team='a' AND run_id='run'`, expiry.UnixNano(), []byte(`{"retry":{"set":true,"value":1}}`))
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "runner", Generation: 2}}
	admitExecution(t, tenant, actor)
	return tenant, now, actor
}

func TestTransitionApplicationRetryKeepsClaimAndOrdinal(t *testing.T) {
	tenant, _, actor := liveHolderFixture(t)
	ctx := context.Background()
	ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
	applied, err := tenant.TransitionNode(ctx, nodeTransition{Ref: ref, To: runstate.NodeRunning, Actor: actor, Cause: runstate.CauseRetry, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: "verify"}}})
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
	result := nodeTransition{Ref: ref, To: runstate.NodeSuccess, Actor: actor, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(4), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}}
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
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels,claimed_at) VALUES ('a','run','claimed','trigger',1,1,'host','owner',1,?,?,?,1)`, expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}, CapabilityPrefix: "owner", Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: "run", Holder: "host", Generation: 1}}}
	change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: runstate.NodeFailed, Actor: actor, Cause: runstate.CauseReport}
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
	if err != nil || row.Status != runstate.StatusRunning || row.StateSeq != 0 {
		t.Fatalf("refused writes changed row: %+v, %v", row, err)
	}
}

func TestSchemaAllowsRetryThenTerminalClaimClearing(t *testing.T) {
	tenant, _, actor := liveHolderFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=?`, []byte(`{"retry":{"set":true,"value":1}}`))
	ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
	retry := nodeTransition{Ref: ref, To: runstate.NodeRunning, Actor: actor, Cause: runstate.CauseRetry, SourceAttempt: sourceAttempt(3), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}}
	if _, err := tenant.TransitionNode(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	finish := nodeTransition{Ref: ref, To: runstate.NodeSuccess, Actor: actor, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(4)}
	if _, err := tenant.TransitionNode(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Status != runstate.StatusDone || row.ClaimedBy != "" || row.CapabilityPrefix != "" || row.LeaseExpiresAt != nil || row.FinishedAt == nil {
		t.Fatalf("terminal row: %+v, %v", row, err)
	}
	var open int
	if err := tenant.s.db.QueryRow(`SELECT count(*) FROM node_attempts WHERE finished_at IS NULL`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open attempts: %d, %v", open, err)
	}
}

func TestHolderFenceCannotBeReboundToAnotherTarget(t *testing.T) {
	for _, target := range []string{"run", "job"} {
		t.Run(target, func(t *testing.T) {
			tenant, _, actor := liveHolderFixture(t)
			ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			if target == "run" {
				actor.RunID = "other-run"
			} else {
				actor.NodeID = "other-job"
			}
			_, err = tenant.TransitionNode(t.Context(), ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(3), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}})
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
