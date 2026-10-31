package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestControllerRetryReusesSuccessfulJobWithoutAnAttempt(t *testing.T) {
	for _, missing := range []string{"", "records", "run", "node"} {
		t.Run("missing="+missing, func(t *testing.T) {
			tenant, plan, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}, {ID: "next", Needs: []string{"job"}}}})
			s := tenant.s
			records := &blobRecords{manifests: fakeBlobManifests{}}
			s.resultRecords, s.materializations = records, records
			rewriteSchemaExec(t, s.db, `UPDATE teams SET high_trust=1 WHERE name='a'`)
			actor := controllerJobActor(t, tenant, plan.RunID)
			startNext := func(run string) runstate.Actor {
				claim, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(run), NodeID: "next"}})
				if err != nil {
					t.Fatal(err)
				}
				reader := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(run), NodeID: "next", Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
				admitExecution(t, tenant, reader)
				if _, err := tenant.TransitionNode(t.Context(), StartNode{Ref: runspec.NodeRef{RunID: runspec.RunID(run), NodeID: "next"}, Actor: reader, SourceAttempt: sourceAttempt(0)}); err != nil {
					t.Fatal(err)
				}
				return reader
			}
			pre, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			intent := blobIntent(actor, runspec.AttemptIdentity{Team: "a", RunID: plan.RunID, NodeID: "job", Attempt: int64(pre.Attempt), Generation: int64(pre.Generation)})
			intent.Authority.Principal, intent.Identity.Principal = "runner", "runner"
			if _, err := commitTestBlob(t, tenant, intent); err != nil {
				t.Fatal(err)
			}
			authority, err := tenant.ResolveBlobAuthority(t.Context(), intent.Authority, capability.DataWrite)
			if err != nil {
				t.Fatal(err)
			}
			output := blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:1", authority.Ref.Scope, "p", blob.KindOutput)
			if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: output, Digest: intent.Digest}); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: pre.Ref, Actor: actor, SourceAttempt: sourceAttempt(pre.Attempt), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess, OutputRecord: &output}}); err != nil {
				t.Fatal(err)
			}
			failed := startNext(plan.RunID)
			if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: "next"}, Actor: failed, SourceAttempt: sourceAttempt(1), Outcome: runstate.NodeFailed}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
				t.Fatal(err)
			}
			retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, IdempotencyKey: "retry", RetryMode: "failed", Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: plan.RunID}}})
			if err != nil {
				t.Fatal(err)
			}
			switch missing {
			case "records":
				rewriteSchemaExec(t, s.db, `DELETE FROM blob_records WHERE team='a' AND run_id=?`, plan.RunID)
			case "run":
				rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE team='a' AND id=?`, plan.RunID)
			case "node":
				rewriteSchemaExec(t, s.db, `DELETE FROM nodes WHERE team='a' AND run_id=? AND node_id='job'`, plan.RunID)
			}
			run := mustReadRun(t, tenant, retry.RunID)
			if run.Retry == nil || run.Retry.Results["job"].Ref != pre.Ref {
				t.Fatalf("retry lost its reuse selection: %+v", run.Retry)
			}
			for range 2 {
				if _, err := s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
					t.Fatal(err)
				}
			}
			reused, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(retry.RunID), NodeID: "job"})
			if missing != "" {
				if err != nil || reused.Status != runstate.StatusPending || reused.ReadyAt == nil || reused.OutputRecord != nil {
					t.Fatalf("missing source did not fall back to execution: %+v, %v", reused, err)
				}
				if _, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(retry.RunID), NodeID: "job"}}); err != nil {
					t.Fatalf("missing source job cannot execute: %v", err)
				}
				return
			}
			if err != nil || reused.Status != runstate.StatusDone || reused.Outcome != runstate.NodeSuccess || reused.Attempt != 0 || reused.OutputRecord == nil {
				t.Fatalf("reused job must finish with output and zero attempts: %+v, %v", reused, err)
			}
			var attempts int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_attempts WHERE team='a' AND run_id=? AND node_id='job'`, retry.RunID).Scan(&attempts); err != nil || attempts != 0 {
				t.Fatalf("reused job attempts=%d: %v", attempts, err)
			}
			if _, err := s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(retry.RunID), NodeID: "job"}}); err == nil {
				t.Fatalf("reused job remained claimable: %v", err)
			}
			rewriteSchemaExec(t, s.db, `DELETE FROM runs WHERE team='a' AND id=?`, plan.RunID)
			reader := startNext(retry.RunID)
			hit, err := tenant.LookupRecord(t.Context(), blob.RecordLookup{Authority: blob.Authority{CapabilityRef: reader.CapabilityPrefix}, Key: *reused.OutputRecord})
			if err != nil || !hit.Found || hit.Record.Digest != intent.Digest {
				t.Fatalf("dependent cannot read reused output: %+v, %v", hit, err)
			}
		})
	}
}
