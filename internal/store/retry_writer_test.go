package store

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func TestRetriedCacheAndMemoStayInSourceWritersScope(t *testing.T) {
	for _, mode := range []string{"all", "failed"} {
		t.Run(mode, func(t *testing.T) {
			s := authorityStore(t)
			tenant := referenceWorld(t, s, "gh:701")
			s.testOnlyExec(t, `DELETE FROM secrets WHERE team='a'`)
			root := authorityRoot("writer-runner", "a")
			root.Grants = []capability.Grant{capability.TriggersClaim, capability.NodesClaim}
			authorityPersist(t, s, root)
			source := manualReference(t, s, "alice")
			sourceRow := mustReadRun(t, tenant, source)
			original := startWriterCacheJob(t, tenant, AdmissionResult{RunID: source, Request: sourceRow.Request})
			attempt := original.Identity.Attempt.Value
			sourceActor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: original.Authority.CapabilityRef}, CapabilityPrefix: original.Authority.CapabilityRef, NodeFence: runspec.NodeFence{RunID: runspec.RunID(source), NodeID: "job", Holder: "writer-runner", Generation: runspec.ClaimGeneration(attempt.Generation)}}
			if _, err := tenant.TransitionNode(t.Context(), ReportNode{Ref: runspec.NodeRef{RunID: runspec.RunID(source), NodeID: "job"}, Actor: sourceActor, Outcome: runstate.NodeFailed, SourceAttempt: sourceAttempt(attempt.Attempt)}); err != nil {
				t.Fatal(err)
			}
			bob := referenceMember(t, s, "bob", RoleEditor)
			retry, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: bob.Authority, RetryMode: mode, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: source}}})
			if err != nil {
				t.Fatal(err)
			}
			intent := startWriterCacheJob(t, tenant, retry)
			if _, err := commitTestBlob(t, tenant, intent); err != nil {
				t.Fatal(err)
			}
			wantRef := blob.RefScope{Ref: "refs/heads/main", Writer: string(runspec.PrincipalWriter("account", "alice"))}
			output := blob.CommitRecordRequest{Authority: intent.Authority, Source: intent.Identity, Key: blob.ResultRecordKey(intent.Identity.Attempt.Value, "gh:701", wantRef, "ci", blob.KindOutput), Digest: intent.Digest}
			if _, err := tenant.CommitRecord(t.Context(), output); err != nil {
				t.Fatalf("retry cannot write the source writer's output scope: %v", err)
			}
			memo := output
			memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
			if record, err := tenant.CommitRecord(t.Context(), memo); err != nil || record.Key.Scope.Ref != wantRef {
				t.Fatalf("retry memo scope: %+v %v", record, err)
			}
			dep := output
			dep.Key = blob.RecordKey{Team: "a", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: "gh:701", Ref: wantRef, Key: "v1"}, Name: "modules"}
			if record, err := tenant.CommitRecord(t.Context(), dep); err != nil || record.Key.Scope.Ref != wantRef {
				t.Fatalf("retry dependency cache scope: %+v %v", record, err)
			}
			s.resultRecords, s.materializations = &blobRecords{}, &blobRecords{}
			attempt = intent.Identity.Attempt.Value
			actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: intent.Authority.CapabilityRef}, CapabilityPrefix: intent.Authority.CapabilityRef, NodeFence: runspec.NodeFence{RunID: runspec.RunID(attempt.RunID), NodeID: "job", Holder: "writer-runner", Generation: runspec.ClaimGeneration(attempt.Generation)}}
			if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: actor.NodeFence.RunID, NodeID: "job"}, Actor: actor, To: runstate.NodeSuccess, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(attempt.Attempt), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess, OutputRecord: &output.Key}}}); err != nil {
				t.Fatal(err)
			}
			for _, account := range []string{"alice", "bob"} {
				next, err := tenant.AdmitRun(t.Context(), IntakeRequest{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: account}, Request: mustReadRun(t, tenant, source).Request.Request})
				if err != nil {
					t.Fatal(err)
				}
				reader := startWriterCacheJob(t, tenant, next)
				found := account == "alice"
				ref := blob.ScopeFor(next.Request.Provenance)
				cache, err := tenant.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: reader.Authority, Key: blob.DepcacheKey{Repo: "gh:701", Ref: ref, Name: "modules", Key: "v1"}})
				if err != nil || cache.Found != found {
					t.Fatalf("%s cache hit=%v, want %v: %v", account, cache.Found, found, err)
				}
				hit, err := tenant.LookupMemo(t.Context(), blob.MemoLookup{Authority: reader.Authority, Key: blob.MemoKey{Repo: "gh:701", Ref: ref, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}})
				if err != nil || hit.Found != found {
					t.Fatalf("%s memo hit=%v, want %v: %v", account, hit.Found, found, err)
				}
			}
		})
	}
}

func startWriterCacheJob(t *testing.T, tenant *Tenant, admitted AdmissionResult) blob.UploadIntent {
	t.Helper()
	claim, err := tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "writer-runner", Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	plan := testBodyPlan(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Memo: &nodeproto.MemoSpec{Input: &nodeproto.MemoInput{Kind: "const", Value: "fixture"}}}}})
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: runspec.CheckoutDeclarations{Version: 1}, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	owner := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	if _, err := tenant.AcceptPlan(t.Context(), PlanAcceptance{RunID: admitted.RunID, Actor: owner, Seal: runspec.SealInput{Request: admitted.Request, Declarations: runspec.CheckoutDeclarations{Version: 1}, Plan: body, Resolved: runspec.Resolved{Args: map[string]string{}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(t.Context(), ReadyNode{Ref: runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "job"}, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	claim, err = tenant.s.AsOperator().ClaimUnit(t.Context(), UnitDemand{CapabilityPrefix: "writer-runner", Target: &UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(admitted.RunID), NodeID: "job"}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(claim.RunID), NodeID: "job", Holder: claim.Award.Holder, Generation: claim.Award.Generation}}
	admitExecution(t, tenant, actor)
	if _, err := tenant.TransitionNode(t.Context(), nodeTransition{Ref: runspec.NodeRef{RunID: actor.NodeFence.RunID, NodeID: "job"}, Actor: actor, To: runstate.NodeRunning, Cause: runstate.CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: actor.NodeFence.RunID, NodeID: actor.NodeFence.NodeID})
	if err != nil {
		t.Fatal(err)
	}
	intent := blobIntent(actor, runspec.AttemptIdentity{Team: "a", RunID: admitted.RunID, NodeID: "job", Attempt: int64(row.Attempt), Generation: int64(row.Generation)})
	intent.Authority.Principal, intent.Identity.Principal = "writer-runner", "writer-runner"
	return intent
}

func mustReadRun(t *testing.T, tenant *Tenant, run string) RunRow {
	t.Helper()
	row, err := tenant.ReadRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
