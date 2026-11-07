package store

import (
	"strings"
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
			// safety: a fresh host writes the shared scope the retry reads its reused output from.
			actor := controllerJobActor(t, tenant, plan.RunID, func(c *capability.Capability) {
				c.Host = &capability.HostFacts{Kind: capability.HostCloud, Fresh: true}
			})
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

type retrySource struct {
	tenant *Tenant
	runID  string
	runner capability.Capability
	reads  fakeBlobManifests
}

// safety: a fresh Cloud host writes each result in the scope a retry reads; the last job fails.
func retrySourceFixture(t *testing.T, jobs []nodeproto.JobSpec, artifacts bool) retrySource {
	t.Helper()
	tenant, plan, _ := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: jobs})
	source := retrySource{tenant: tenant, runID: plan.RunID, reads: fakeBlobManifests{}}
	records := &blobRecords{manifests: source.reads}
	tenant.s.resultRecords, tenant.s.materializations = records, records
	source.runner = authorityRoot("runner", "a")
	source.runner.Grants = []capability.Grant{capability.NodesClaim}
	source.runner.Host = &capability.HostFacts{Kind: capability.HostCloud, Fresh: true}
	authorityPersist(t, tenant.s, source.runner)
	fundUnitClaim(t, tenant.s)
	for index, job := range jobs {
		ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: runspec.NodeID(job.ID)}
		claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: source.runner.Prefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}})
		if err != nil {
			t.Fatalf("claim %s: %v", job.ID, err)
		}
		actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
		admitExecution(t, tenant, actor)
		if _, err := tenant.TransitionNode(t.Context(), StartNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
		if index == len(jobs)-1 {
			if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(1), Outcome: runstate.NodeFailed}); err != nil {
				t.Fatal(err)
			}
			break
		}
		row, err := tenant.ReadNode(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		intent := blobIntent(actor, runspec.AttemptIdentity{Team: "a", RunID: plan.RunID, NodeID: job.ID, Attempt: int64(row.Attempt), Generation: int64(row.Generation)})
		intent.Authority.Principal, intent.Identity.Principal = "runner", "runner"
		authority, err := tenant.ResolveBlobAuthority(t.Context(), intent.Authority, capability.DataWrite)
		if err != nil {
			t.Fatal(err)
		}
		digest := func(kind byte) blob.Digest {
			return blob.Digest(strings.Repeat(string("0123456789"[index]), 63) + string(kind))
		}
		commit := func(d blob.Digest) {
			intent.Digest = d
			if _, err := commitTestBlob(t, tenant, intent); err != nil {
				t.Fatal(err)
			}
		}
		result := &runstate.NodeResult{Outcome: runstate.NodeSuccess}
		commit(digest('a'))
		output := blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:1", authority.Ref.Scope, "p", blob.KindOutput)
		if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: output, Digest: digest('a')}); err != nil {
			t.Fatal(err)
		}
		result.OutputRecord = &output
		if artifacts {
			member := blob.Member{Team: "a", ManifestDigest: digest('b'), Digest: digest('c'), Path: "artifact", Size: 3}
			source.reads[member.ManifestDigest] = []blob.Member{member}
			commit(member.Digest)
			commit(member.ManifestDigest)
			manifest := blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:1", authority.Ref.Scope, "p", blob.KindManifest)
			if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: manifest, Digest: member.ManifestDigest, Members: []blob.Member{member}}); err != nil {
				t.Fatal(err)
			}
			result.ManifestRecord = &manifest
		}
		if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: ref, Actor: actor, SourceAttempt: sourceAttempt(row.Attempt), Outcome: runstate.NodeSuccess, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
		t.Fatal(err)
	}
	return source
}

func (source retrySource) retry(t *testing.T, mode string) string {
	t.Helper()
	retry, err := source.tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"}, IdempotencyKey: "retry-" + mode, RetryMode: mode, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: source.runID}}})
	if err != nil {
		t.Fatal(err)
	}
	return retry.RunID
}

// Each reused level adds a deferred read round, so reuse that waits for its
// dependencies must still finish a chain longer than the store's read retries.
func TestControllerRetryReusesAChainOfStoredResults(t *testing.T) {
	chain := []nodeproto.JobSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"b"}}, {ID: "d", Needs: []string{"c"}}, {ID: "failed", Needs: []string{"d"}}}
	pair := []nodeproto.JobSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "failed", Needs: []string{"b"}}}
	for name, shape := range map[string]struct {
		jobs      []nodeproto.JobSpec
		artifacts bool
	}{"four outputs": {chain, false}, "two artifact manifests": {pair, true}} {
		t.Run(name, func(t *testing.T) {
			source := retrySourceFixture(t, shape.jobs, shape.artifacts)
			runID := source.retry(t, "failed")
			if _, err := source.tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
				t.Fatal(err)
			}
			for _, job := range shape.jobs {
				row, err := source.tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(runID), NodeID: runspec.NodeID(job.ID)})
				if err != nil {
					t.Fatal(err)
				}
				if job.ID == "failed" {
					if row.Status != runstate.StatusPending || row.ReadyAt == nil {
						t.Fatalf("failed job was not released after its reused dependencies: %+v", row)
					}
					continue
				}
				if row.Status != runstate.StatusDone || row.Outcome != runstate.NodeSuccess || row.Attempt != 0 || row.OutputRecord == nil || shape.artifacts && row.ManifestRecord == nil {
					t.Fatalf("job %s was not reused: %+v", job.ID, row)
				}
			}
		})
	}
}

// A refused source must not stop the controller sweep, which would hold every
// run after it, so the job runs again instead.
func TestControllerRetryRunsARefusedReuseAgainAndKeepsSweeping(t *testing.T) {
	source := retrySourceFixture(t, []nodeproto.JobSpec{{ID: "a"}, {ID: "failed", Needs: []string{"a"}}}, false)
	refused := source.retry(t, "failed")
	other := source.retry(t, "all")
	rewriteSchemaExec(t, source.tenant.s.db, `UPDATE nodes SET spec_hash='changed' WHERE team='a' AND run_id=? AND node_id='a'`, source.runID)
	if _, err := source.tenant.s.AsOperator().ReconcileControllerRuns(t.Context(), "", 10); err != nil {
		t.Fatalf("a refused reuse stopped the sweep: %v", err)
	}
	for _, runID := range []string{refused, other} {
		run := mustReadRun(t, source.tenant, runID)
		row, err := source.tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: runspec.RunID(runID), NodeID: "a"})
		if err != nil || run.State != runstate.RunRunning || row.Status != runstate.StatusPending || row.ReadyAt == nil || row.OutputRecord != nil {
			t.Fatalf("run %s did not advance to execution: %s %+v %v", runID, run.State, row, err)
		}
	}
}
