package store

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func approvalEvaluationFixture(t *testing.T) (*Tenant, controllerwire.NodeRef, UnitClaim) {
	t.Helper()
	tenant, request, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"skip"}}}, {ID: "next", Needs: []string{"gate"}}}})
	root := authorityRoot("approval-evaluator", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	fundUnitClaim(t, tenant.s)
	ref := controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "gate"}
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitEval, RunID: ref.RunID, NodeID: ref.NodeID, SourceGeneration: 0}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}})
	if err != nil {
		t.Fatal(err)
	}
	return tenant, ref, claim
}

func approvalEvaluationActor(claim UnitClaim) Actor {
	return Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
}

func TestControllerApprovalPredicateSettlesUnderItsEvaluationFence(t *testing.T) {
	for _, outcome := range []string{"open", "skip", "fail"} {
		t.Run(outcome, func(t *testing.T) {
			tenant, ref, claim := approvalEvaluationFixture(t)
			actor := approvalEvaluationActor(claim)
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || !pendingApprovalPredicate(row) || row.Generation != 0 || claim.SourceGeneration != 0 {
				t.Fatalf("approval was released as execution: row=%+v claim=%+v error=%v", row, claim, err)
			}
			if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
				live, err := launcherEvalLiveTx(t.Context(), tx, LauncherAward{Node: row, Claim: claim})
				if err != nil {
					return err
				}
				if !live {
					return errors.New("pending approval unavailable to launcher")
				}
				prefixes, err := currentRunnerAwardsTx(t.Context(), tx, tenant.team)
				if err != nil {
					return err
				}
				if !slices.Contains(prefixes, claim.Award.CapabilityPrefix) {
					return errors.New("pending approval missing from runner inventory")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rewriteSchemaCount(t, tenant.s.db, "concurrency_holders", 0)
			rewriteSchemaCount(t, tenant.s.db, "concurrency_waiters", 0)
			rewriteSchemaCount(t, tenant.s.db, "approvals", 0)
			change := nodeTransition{Ref: ref, Actor: actor, To: NodeApprovalPending, Cause: CauseApprovalOpen}
			if outcome != "open" {
				kind, state, reason := PolicyApprovalBypass, NodeSkipped, "skip_if"
				if outcome == "fail" {
					kind, state, reason = PolicyApprovalFail, NodeFailed, "predicate failed"
				}
				change.To, change.Cause, change.Extra = state, causeOwnerPolicy, transitionExtra{OwnerPolicy: &OwnerPolicyResult{Kind: kind, Generation: 0}, Result: &NodeResult{Outcome: state, Reason: reason}}
			}
			wrong := change
			wrong.Ref.NodeID = "next"
			if _, err := tenant.TransitionNode(t.Context(), wrong); err == nil {
				t.Fatal("approval evaluator mutated another job")
			}
			wrong = change
			wrong.Actor.Owner.SourceGeneration++
			if _, err := tenant.TransitionNode(t.Context(), wrong); err == nil {
				t.Fatal("changed evaluation source fence accepted")
			}
			if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			row, err = tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Attempt != 0 || outcome == "open" && row.Status != StatusApprovalPending || outcome == "skip" && row.Outcome != NodeSkipped || outcome == "fail" && row.Outcome != NodeFailed {
				t.Fatalf("approval outcome lost: %+v %v", row, err)
			}
			var status string
			if err := tenant.s.db.QueryRow(`SELECT status FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=0`, tenant.team, ref.RunID, ref.NodeID).Scan(&status); err != nil || status != "done" {
				t.Fatalf("evaluation did not settle atomically: %s %v", status, err)
			}
			next, err := tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: ref.RunID, NodeID: "next"})
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "skip" && next.ReadyAt == nil || outcome == "fail" && next.Status != StatusDone {
				t.Fatalf("approval outcome did not reach shared Decide owner: %+v", next)
			}
			rewriteSchemaCount(t, tenant.s.db, "concurrency_holders", 0)
			rewriteSchemaCount(t, tenant.s.db, "node_attempts", 0)
		})
	}
}

func TestControllerApprovalPredicateRecoversAnExpiredEvaluation(t *testing.T) {
	tenant, ref, claim := approvalEvaluationFixture(t)
	now := tenant.s.now()
	rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=?`, now.UnixNano(), tenant.team, ref.RunID, ref.NodeID)
	actor := approvalEvaluationActor(claim)
	if _, err := tenant.TransitionNode(t.Context(), OpenApprovalNode{Ref: ref, Actor: actor}); err == nil {
		t.Fatal("expired evaluator opened approval")
	}
	lease := maintenanceReaper(t, tenant.s)
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
		t.Fatalf("approval evaluator did not recover: %+v %v", reaped, err)
	}
	demand := UnitDemand{CapabilityPrefix: "approval-evaluator", Target: &UnitRef{Kind: capability.UnitEval, RunID: ref.RunID, NodeID: ref.NodeID, SourceGeneration: 0}, Capacity: match.Resources{Cores: 8}, Available: match.Resources{Cores: 8}}
	replacement, err := tenant.s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil || replacement.Award.Generation <= claim.Award.Generation {
		t.Fatalf("replacement evaluator: %+v %v", replacement, err)
	}
	if _, err := tenant.TransitionNode(t.Context(), OpenApprovalNode{Ref: ref, Actor: actor}); err == nil {
		t.Fatal("replaced evaluator committed approval")
	}
	if _, err := tenant.TransitionNode(t.Context(), OpenApprovalNode{Ref: ref, Actor: approvalEvaluationActor(replacement)}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerApprovalVerdictRollsBackWithEvaluationCompletion(t *testing.T) {
	tenant, ref, claim := approvalEvaluationFixture(t)
	actor := approvalEvaluationActor(claim)
	rollback := errors.New("caller rollback")
	err := tenant.s.inBlobTx(t.Context(), func(tx *storeTx) error {
		if _, err := transitionNodeTx(t.Context(), tx, tenant.team, OpenApprovalNode{Ref: ref, Actor: actor}.nodeChange()); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || !pendingApprovalPredicate(row) {
		t.Fatalf("rolled-back predicate opened approval: %+v %v", row, err)
	}
	var status string
	if err := tenant.s.db.QueryRow(`SELECT status FROM eval_units WHERE team=? AND run_id=? AND source_node_id=?`, tenant.team, ref.RunID, ref.NodeID).Scan(&status); err != nil || status != "claimed" {
		t.Fatalf("rolled-back predicate closed evaluation: %s %v", status, err)
	}
	if _, err := tenant.TransitionNode(t.Context(), OpenApprovalNode{Ref: ref, Actor: actor}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerApprovalPredicateSettlesExhaustedEvaluator(t *testing.T) {
	tenant, ref, _ := approvalEvaluationFixture(t)
	now := tenant.s.now()
	rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET lease_expires_at=?,started_at=?,disruptions=2 WHERE team=? AND run_id=? AND source_node_id=?`, now.UnixNano(), now.Add(-time.Second).UnixNano(), tenant.team, ref.RunID, ref.NodeID)
	lease := maintenanceReaper(t, tenant.s)
	reaped, err := tenant.s.AsOperator().ReapUnits(t.Context(), lease, 10)
	if err != nil || len(reaped) != 1 || !reaped[0].Failed {
		t.Fatalf("exhausted evaluator: %+v %v", reaped, err)
	}
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Status != StatusDone || row.Outcome != NodeFailed || row.Attempt != 0 {
		t.Fatalf("exhausted approval remained pending: %+v %v", row, err)
	}
}

func TestApprovalEvaluationReadsOnlyDeclaredDependencyOutputs(t *testing.T) {
	tenant, ref, claim := approvalEvaluationFixture(t)
	var gate nodeproto.JobSpec
	row, err := tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(row.Spec, &gate); err != nil {
		t.Fatal(err)
	}
	gate.Needs = []string{"producer"}
	raw, err := json.Marshal(gate)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET spec_json=? WHERE team=? AND run_id=? AND node_id=?`, raw, tenant.team, ref.RunID, ref.NodeID)
	run, err := tenant.ReadRun(t.Context(), string(ref.RunID))
	if err != nil {
		t.Fatal(err)
	}
	var digests []blob.Digest
	for i, id := range []string{"producer", "sibling"} {
		attempt := runspec.AttemptIdentity{Team: string(tenant.team), RunID: string(ref.RunID), NodeID: id, Attempt: 1, Generation: 3}
		key := blob.ResultRecordKey(attempt, run.Request.Request.Git.Repository.Key, blob.ScopeFor(run.Request.Provenance), run.Request.Request.Pipeline, blob.KindOutput)
		selected, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := json.Marshal(nodeproto.JobSpec{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO nodes(team,run_id,node_id,status,outcome,generation,attempt,finished_at,spec_json,execution_json,output_record) VALUES(?,?,?,'done','success',3,1,1,?,?,?)`, tenant.team, ref.RunID, id, spec, []byte(`{}`), selected)
		digest := blob.Digest(strings.Repeat([]string{"a", "b"}[i], 64))
		digests = append(digests, digest)
		rewriteSchemaExec(t, tenant.s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES(?,?,3,1)`, tenant.team, digest)
		source := blob.UploadIdentity{Kind: blob.UploadJob, Team: string(tenant.team), RunID: string(ref.RunID), Principal: blob.Principal(id), Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
		namespace, err := blob.RecordNamespace(key)
		if err != nil {
			t.Fatal(err)
		}
		if err := tenant.s.inTx(t.Context(), func(tx *storeTx) error {
			return persistBlobRecordTx(t.Context(), tx, blob.Record{Key: key, Digest: digest, Source: source, Provenance: blob.ProvenanceLocal, RefFacts: blob.RefFacts{Scope: key.Scope.Ref}}, namespace, source, nil)
		}); err != nil {
			t.Fatal(err)
		}
	}
	principal, err := tenant.s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"producer", "sibling"} {
		err := tenant.s.AsOperator().CheckBinding(t.Context(), principal, capability.BindReadSet, capability.Target{Team: string(tenant.team), RunID: string(ref.RunID), NodeID: id, SourceGeneration: 3})
		if (err == nil) != (id == "producer") {
			t.Fatalf("approval output %s: %v", id, err)
		}
	}
	present, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: blob.Authority{CapabilityRef: claim.Award.CapabilityPrefix}, Digests: digests})
	if err != nil || len(present.Present) != 1 || present.Present[0] != digests[0] {
		t.Fatalf("approval read-set escaped accepted dependency: %+v %v", present, err)
	}
}
