package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type materializationRecordsFunc func(context.Context, *storeTx, Team, Actor, controllerwire.NodeRef, MaterializeSource) (materializedRecords, error)

func (f materializationRecordsFunc) CopyMaterializedRecords(ctx context.Context, tx *storeTx, team Team, actor Actor, target controllerwire.NodeRef, source MaterializeSource) (materializedRecords, error) {
	return f(ctx, tx, team, actor, target, source)
}

func materializationFixture(t *testing.T, policy SourcePolicy) (*Tenant, ReconcileRequest) {
	t.Helper()
	tenant, _, actor := reconcileFixture(t)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',claimed_by=NULL,capability_prefix='',lease_expires_at=NULL,started_at=NULL,generation=0,attempt=0,spec_hash='same'`)
	maintenanceRun(t, tenant.s, "source", 2, RunSuccess)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET lineage_json=? WHERE id='run'`, []byte(`{"retry_of":"source","retry":{"mode":"failed","results":{"job":{"Ref":{"run_id":"source","job_id":"job"},"StateSeq":1}}}}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,attempt,finished_at,spec_json,spec_hash,execution_json) VALUES ('a','source','job','done','success',2,1,1,?,'same',?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	rewriteSchemaExec(t, tenant.s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES ('a','source','job',1,2,'past','past','runner','past','remote',1,2,'success')`)
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET state_seq=1 WHERE run_id='source'`)
	source := MaterializeSource{Kind: policy, Source: runspec.AttemptIdentity{Team: "a", RunID: "source", NodeID: "job", Attempt: 1, Generation: 2}}
	if policy == SourceMemo {
		source.Record = blob.RecordKey{Team: "a", Kind: blob.KindMemo, Scope: blob.Scope{Repo: "gh:1", Pipeline: "p", Job: "job"}, Name: "key"}
	}
	tenant.s.materializations = materializationRecordsFunc(func(_ context.Context, _ *storeTx, team Team, got Actor, ref controllerwire.NodeRef, s MaterializeSource) (materializedRecords, error) {
		if team != "a" || got != actor || ref != (controllerwire.NodeRef{RunID: "run", NodeID: "job"}) || s != source {
			t.Fatal("record copier did not receive the authenticated source and destination")
		}
		return materializedRecords{}, nil
	})
	return tenant, ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, Policy: policy, Source: &source}
}

func TestReconcileMaterializesPersistedOutcomeOnce(t *testing.T) {
	for _, policy := range []SourcePolicy{SourceManualRetry, SourceMemo, SourceCoalesced} {
		t.Run(string(policy), func(t *testing.T) {
			tenant, request := materializationFixture(t, policy)
			want := NodeSuccess
			if policy == SourceMemo {
				want = NodeMemoized
			}
			got, err := tenant.ReconcileNode(t.Context(), request)
			if err != nil || got.Kind != Materialize || got.Node.Outcome != want || got.Claim != nil || got.Node.StateSeq != 1 || got.Node.ClaimedBy != "" {
				t.Fatalf("materialize: %+v, %v", got, err)
			}
			got, err = tenant.ReconcileNode(t.Context(), request)
			if err != nil || got.Kind != KeepTerminal {
				t.Fatalf("repeat: %+v, %v", got, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 1)
		})
	}
}

func TestReconcileMaterializationRefusesInvalidSource(t *testing.T) {
	for _, problem := range []string{"lineage", "spec", "attempt", "generation", "source_running", "failed_retry", "foreign_team", "copy_unavailable", "forbidden_edge", "cancelled"} {
		t.Run(problem, func(t *testing.T) {
			tenant, request := materializationFixture(t, SourceManualRetry)
			switch problem {
			case "lineage":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET lineage_json=? WHERE id='run'`, []byte(`{}`))
			case "spec":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_hash='changed' WHERE run_id='source'`)
			case "attempt":
				request.Source.Source.Attempt++
			case "generation":
				request.Source.Source.Generation++
			case "source_running":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',outcome='',finished_at=NULL WHERE run_id='source'`)
			case "failed_retry":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET outcome='failed' WHERE run_id='source'`)
			case "foreign_team":
				request.Source.Source.Team = "b"
			case "copy_unavailable":
				tenant.s.materializations = nil
			case "forbidden_edge":
				tenant.s.materializations = materializationRecordsFunc(func(context.Context, *storeTx, Team, Actor, controllerwire.NodeRef, MaterializeSource) (materializedRecords, error) {
					return materializedRecords{}, errors.New("source record is not readable")
				})
			case "cancelled":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
			}
			if _, err := tenant.ReconcileNode(t.Context(), request); err == nil {
				t.Fatal("invalid source materialized")
			}
			node, err := tenant.ReadNode(t.Context(), request.Ref)
			if err != nil || node.StateSeq != 0 || node.Status != StatusPending {
				t.Fatalf("refused materialization changed job: %+v, %v", node, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 0)
		})
	}
}

func TestMaterializationCannotSupplyAnOutcome(t *testing.T) {
	tenant, request := materializationFixture(t, SourceManualRetry)
	for _, extra := range []transitionExtra{{Materialize: request.Source, Result: &NodeResult{Outcome: NodeFailed}}, {Materialize: request.Source}} {
		_, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: request.Ref, Actor: request.Actor, Cause: CauseMaterialize, To: NodeFailed, Extra: extra})
		if err == nil {
			t.Fatal("caller selected materialization outcome")
		}
	}
	rewriteSchemaCount(t, tenant.s.db, "events", 0)
}

func TestReconcileMemoSurvivesSourceRunRetention(t *testing.T) {
	tenant, request := materializationFixture(t, SourceMemo)
	rewriteSchemaExec(t, tenant.s.db, `DELETE FROM runs WHERE id='source'`)
	got, err := tenant.ReconcileNode(t.Context(), request)
	if err != nil || got.Kind != Materialize || got.Node.Outcome != NodeMemoized {
		t.Fatalf("retained memo: %+v, %v", got, err)
	}
}

func TestReconcileRecordCopyRollsBackWithState(t *testing.T) {
	tenant, request := materializationFixture(t, SourceMemo)
	copyErr := errors.New("corrupt manifest member")
	tenant.s.materializations = materializationRecordsFunc(func(ctx context.Context, tx *storeTx, _ Team, _ Actor, _ controllerwire.NodeRef, _ MaterializeSource) (materializedRecords, error) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES ('copied',?)`, []byte(`yes`)); err != nil {
			return materializedRecords{}, err
		}
		return materializedRecords{}, copyErr
	})
	if _, err := tenant.ReconcileNode(t.Context(), request); !errors.Is(err, copyErr) {
		t.Fatalf("copy failure: %v", err)
	}
	node, err := tenant.ReadNode(t.Context(), request.Ref)
	if err != nil || node.Status != StatusPending || node.StateSeq != 0 {
		t.Fatalf("copy failure changed job state: %+v, %v", node, err)
	}
	assertResultSelectors(t, node, materializedRecords{})
	rewriteSchemaCount(t, tenant.s.db, "settings", 0)
	rewriteSchemaCount(t, tenant.s.db, "events", 0)
}

func TestReconcileSourceDoesNotStealHeldWork(t *testing.T) {
	for _, kind := range []string{"dispatched", "running", "expired"} {
		t.Run(kind, func(t *testing.T) {
			tenant, request := materializationFixture(t, SourceManualRetry)
			owner := "owner"
			if kind == "dispatched" {
				owner = ""
			}
			lease := tenant.s.now().Add(time.Minute)
			if kind == "expired" {
				lease = tenant.s.now()
			}
			rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET claimed_by='host',capability_prefix='held',owner_capability_prefix=?,generation=1,lease_expires_at=? WHERE run_id='run'`, owner, lease.UnixNano())
			if kind == "running" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='running',attempt=1,started_at=1 WHERE run_id='run'`)
			}
			got, err := tenant.ReconcileNode(t.Context(), request)
			if err != nil || got.Kind != WaitHolder || got.Node.StateSeq != 0 {
				t.Fatalf("held %s: %+v, %v", kind, got, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", 0)
		})
	}
}

func TestReconcileRetryCanReuseMaterializedResult(t *testing.T) {
	for _, scenario := range []string{"retry", "memo", "memo_after_loss"} {
		t.Run(scenario, func(t *testing.T) {
			policy := SourceManualRetry
			if scenario != "retry" {
				policy = SourceMemo
			}
			tenant, request := materializationFixture(t, policy)
			if scenario == "memo_after_loss" {
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=1,attempt=1,disruptions=1 WHERE run_id='run'`)
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome,failure_reason) VALUES ('a','run','job',1,1,'lost','lost','runner','lost','remote',1,2,'failed','agent_lost')`)
			}
			first, err := tenant.ReconcileNode(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			maintenanceRun(t, tenant.s, "next", 3, RunRunning)
			rewriteSchemaExec(t, tenant.s.db, `UPDATE runs SET lineage_json=? WHERE id='next'`, []byte(`{"retry_of":"run","retry":{"mode":"failed","results":{"job":{"Ref":{"run_id":"run","job_id":"job"},"StateSeq":1}}}}`))
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,spec_hash,execution_json) VALUES ('a','next','job','pending',?,'same',?)`, []byte(`{"id":"job"}`), []byte(`{}`))
			expiry := tenant.s.now().Add(time.Hour)
			owner := authorityRoot("next-owner", "a")
			owner.Subject.ID = "host"
			owner.Binding = capability.Binding{Kind: capability.BindingTrigger, RunID: "next", Holder: "host", Generation: 1}
			owner.ExpiresAt = &expiry
			authorityPersist(t, tenant.s, owner)
			rewriteSchemaExec(t, tenant.s.db, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,needs_labels,prefers_labels) VALUES ('a','next','claimed','trigger',1,1,'host','next-owner',1,?,?,?)`, expiry.UnixNano(), []byte(`[]`), []byte(`[]`))
			actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "next-owner"}, CapabilityPrefix: "next-owner", Owner: OwnerLeaseRef{Kind: OwnerTrigger, RunID: "next", Holder: "host", Generation: 1}}
			source := MaterializeSource{Kind: SourceManualRetry, Result: &MaterializedResultSource{Ref: first.Node.Ref, StateSeq: first.Node.StateSeq}}
			tenant.s.materializations = materializationRecordsFunc(func(context.Context, *storeTx, Team, Actor, controllerwire.NodeRef, MaterializeSource) (materializedRecords, error) {
				return materializedRecords{}, nil
			})
			got, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: controllerwire.NodeRef{RunID: "next", NodeID: "job"}, Actor: actor, Policy: SourceManualRetry, Source: &source})
			if err != nil || got.Kind != Materialize || got.Node.Outcome != first.Node.Outcome || got.Node.Attempt != 0 {
				t.Fatalf("second retry: %+v, %v", got, err)
			}
		})
	}
}
