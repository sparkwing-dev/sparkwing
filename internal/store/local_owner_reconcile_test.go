package store

import (
	"context"
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLocalAcceptedPlanReconcilesIndependentJobAwards(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tenant, in, _ := intakeFixture(t)
	s := tenant.s
	root := authorityRoot("local-owner", "a")
	root.Subject.Kind = capability.SubjectLocal
	root.Grants = []capability.Grant{capability.RunsWrite, capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, s, root)
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	in.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	in.Request.Origin = runspec.OriginLocal
	in.Request.Git.Repository.Key = "git:example.com/repo"
	admitted, err := tenant.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	award, err := s.AsOperator().ClaimUnit(ctx, UnitDemand{CapabilityPrefix: root.Prefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Award.CapabilityPrefix}, CapabilityPrefix: award.Award.CapabilityPrefix, Owner: award.Owner}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "first", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}}, {ID: "second", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Needs: []string{"first"}}}}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tenant.AcceptPlan(ctx, PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Nodes {
		if row.OwnerExecution == nil || !row.OwnerExecution.Allowed {
			t.Fatalf("job %s unavailable to owner: %+v", row.Ref.NodeID, row.OwnerExecution)
		}
	}
	var first string
	for _, id := range []runspec.NodeID{"first", "second"} {
		ref := controllerwire.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: id}
		result, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: ref, Actor: actor, Policy: SourceNone})
		if err != nil || result.Kind != Execute || result.Claim == nil {
			t.Fatalf("claim %s: kind=%v err=%v", id, result.Kind, err)
		}
		if first == result.Claim.CapabilityPrefix {
			t.Fatal("jobs reused capability")
		}
		first = result.Claim.CapabilityPrefix
		holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: first}, CapabilityPrefix: first, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: result.Claim.Holder, Generation: result.Claim.Generation}}
		permission, err := tenant.MayStart(ctx, CapacityBinding{RunID: admitted.RunID, NodeID: string(id), Generation: int64(result.Claim.Generation)}, holder)
		if err != nil || permission.Kind != AcquireGranted {
			t.Fatalf("admit %s: %+v %v", id, permission, err)
		}
		if _, err := tenant.TransitionNode(ctx, StartNode{Ref: ref, Actor: holder, SourceAttempt: sourceAttempt(0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.TransitionNode(ctx, ReportNode{Ref: ref, Actor: holder, Outcome: NodeSuccess, SourceAttempt: sourceAttempt(1)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, admitted.RunID); err != nil {
			t.Fatal(err)
		}
	}

}
