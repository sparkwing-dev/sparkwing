package store

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func approvalEvaluationFixture(t *testing.T, prepare ...func(*Tenant, *PlanAcceptance)) (*Tenant, controllerwire.NodeRef, UnitClaim) {
	t.Helper()
	tenant, request, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"skip"}}}, {ID: "next", Needs: []string{"gate"}}}}, prepare...)
	root := authorityRoot("approval-evaluator", "a")
	root.Grants = []capability.Grant{capability.NodesClaim}
	authorityPersist(t, tenant.s, root)
	fundUnitClaim(t, tenant.s)
	ref := controllerwire.NodeRef{RunID: runspec.RunID(request.RunID), NodeID: "gate"}
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitEval, RunID: ref.RunID, NodeID: ref.NodeID, SourceGeneration: 0}})
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
	demand := UnitDemand{CapabilityPrefix: "approval-evaluator", Target: &UnitRef{Kind: capability.UnitEval, RunID: ref.RunID, NodeID: ref.NodeID, SourceGeneration: 0}}
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
		route, ok := capability.LookupRoute("GET", "/api/v1/runs/{run}/jobs/{job}/output")
		if !ok {
			t.Fatal("missing output route")
		}
		target := capability.Target{Team: string(tenant.team), RunID: string(ref.RunID), NodeID: id, SourceGeneration: 3}
		authenticated, err := capability.NewAuthenticator(tenant.s.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: string(claim.Bearer), Evidence: capability.RequestEvidence{Target: target}}, route)
		if err == nil {
			_, err = capability.Authorize(t.Context(), tenant.s.AsOperator(), route, authenticated, target)
		}
		if (err == nil) != (id == "producer") {
			t.Fatalf("authenticated approval output %s: %v", id, err)
		}
	}
	present, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: blob.Authority{CapabilityRef: claim.Award.CapabilityPrefix}, Digests: digests})
	if err != nil || len(present.Present) != 1 || present.Present[0] != digests[0] {
		t.Fatalf("approval read-set escaped accepted dependency: %+v %v", present, err)
	}
}

func TestApprovalEvaluationSecretAndReferenceReadsRequireDeclaredLiveAuthority(t *testing.T) {
	for _, change := range []string{"live", "undeclared_secret", "undeclared_reference", "eval_holder", "eval_generation", "source_generation", "eval_expiry", "controller_generation", "controller_expiry"} {
		t.Run(change, func(t *testing.T) {
			tenant, ref, claim := approvalEvaluationFixture(t, func(tenant *Tenant, request *PlanAcceptance) {
				var plan nodeproto.PlanDoc
				if err := json.Unmarshal(request.Seal.Plan, &plan); err != nil {
					t.Fatal(err)
				}
				plan.Jobs[0].References = []nodeproto.PipelineReference{{Pipeline: "upstream", Job: "artifact"}}
				var err error
				request.Seal.Plan, err = json.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				request.Seal.Resolved.SecretNames = []string{"TOKEN"}
				rewriteSchemaExec(t, tenant.s.db, `INSERT INTO secrets(team,name,pipeline,value,principal,masked,created_at,updated_at) VALUES ('a','TOKEN','p','encrypted','owner',0,1,1)`)
			})
			switch change {
			case "eval_holder":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET claimed_by='replacement' WHERE team=? AND run_id=?`, tenant.team, ref.RunID)
			case "eval_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET generation=generation+1 WHERE team=? AND run_id=?`, tenant.team, ref.RunID)
			case "source_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET generation=generation+1 WHERE team=? AND run_id=? AND node_id=?`, tenant.team, ref.RunID, ref.NodeID)
			case "eval_expiry":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE eval_units SET lease_expires_at=? WHERE team=? AND run_id=?`, tenant.s.now().UnixNano(), tenant.team, ref.RunID)
			case "controller_generation":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET generation=generation+1 WHERE name='controller.owner'`)
			case "controller_expiry":
				rewriteSchemaExec(t, tenant.s.db, `UPDATE leases SET until=? WHERE name='controller.owner'`, tenant.s.now().UnixNano())
			}
			for _, read := range []struct {
				path     string
				target   capability.Target
				declared bool
			}{
				{"/api/v1/secrets/{name}", capability.Target{Team: string(tenant.team), SecretName: "TOKEN"}, change != "undeclared_secret"},
				{"/api/v1/pipelines/{name}/latest", capability.Target{Team: string(tenant.team), Reference: &capability.PipelineReferenceTarget{Pipeline: "upstream", JobID: "artifact"}}, change != "undeclared_reference"},
			} {
				if !read.declared {
					if read.target.SecretName != "" {
						read.target.SecretName = "OTHER"
					} else {
						read.target.Reference.Pipeline = "other"
					}
				}
				route, ok := capability.LookupRoute("GET", read.path)
				if !ok {
					t.Fatal("missing read route")
				}
				op := tenant.s.AsOperator()
				authenticated, err := capability.NewAuthenticator(op).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: string(claim.Bearer), Evidence: capability.RequestEvidence{Target: read.target}}, route)
				if err == nil {
					_, err = capability.Authorize(t.Context(), op, route, authenticated, read.target)
				}
				allowed := read.declared && (change == "live" || change == "undeclared_secret" || change == "undeclared_reference")
				if (err == nil) != allowed {
					t.Fatalf("path=%s allowed=%v error=%v", read.path, allowed, err)
				}
			}
		})
	}
}

func TestGeneratorEvaluationCannotReadApprovalSecretsOrPipelineReferences(t *testing.T) {
	s, _, _, issued := zeroEvalFixture(t)
	for _, target := range []capability.Target{{Team: "a", SecretName: "TOKEN"}, {Team: "a", Reference: &capability.PipelineReferenceTarget{Pipeline: "upstream", JobID: "artifact"}}} {
		rule := capability.BindReadSet
		if target.SecretName != "" {
			rule = capability.BindDeclaredSecret
		}
		principal, err := s.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
		if err != nil {
			t.Fatal(err)
		}
		err = s.AsOperator().CheckBinding(t.Context(), principal, rule, target)
		var refusal *apierr.Error
		if !errors.As(err, &refusal) || refusal.Details["reason"] != apierr.ReasonBinding {
			t.Fatalf("generator reached declaration lookup: target=%+v error=%v", target, err)
		}
	}
}

func TestApprovalEvaluationQueuedClaimUsesPendingSource(t *testing.T) {
	tenant, request, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "gate", Approval: &nodeproto.ApprovalSpec{}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"skip"}}}}})
	root := authorityRoot("queued-approval-evaluator", "a")
	authorityPersist(t, tenant.s, root)
	fundUnitClaim(t, tenant.s)
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: root.Prefix, Kinds: []capability.UnitKind{capability.UnitEval}})
	if err != nil || claim.RunID != request.RunID || claim.NodeID != "gate" || claim.SourceGeneration != 0 {
		t.Fatalf("queued approval claim=%+v: %v", claim, err)
	}
}
