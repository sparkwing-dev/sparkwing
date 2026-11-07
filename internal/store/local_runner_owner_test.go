package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLocalRunnerRollStopsOwnerSelfClaimsAndPreservesHeldWork(t *testing.T) {
	ctx := t.Context()
	s := localRunnerStore(t)
	tenant, in, _ := intakeFixture(t, s)
	tenant.team, in.Request.Team = DefaultTeam, string(DefaultTeam)
	in.Request.Origin = runspec.OriginLocal
	in.Request.Git.Repository.Key = "git:example.com/repo"
	s.claimProfiles = claimProfileFake{profile: match.Profile{}}
	first := registerLocalRunner(t, s, "first")
	second := registerLocalRunner(t, s, "second")
	principal, err := s.AsOperator().ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: first.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	in.Authority = *principal.Capability.Parent
	admitted, err := tenant.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().ActivateLocalRunner(ctx, localRunnerSubject, first.ID); err != nil {
		t.Fatal(err)
	}
	owner, err := s.AsOperator().ClaimUnit(ctx, UnitDemand{CapabilityPrefix: first.CapabilityPrefix, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: owner.Award.CapabilityPrefix}, CapabilityPrefix: owner.Award.CapabilityPrefix, Owner: owner.Owner}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "held", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Children: []nodeproto.ChildPipeline{{Pipeline: "child"}}}, {ID: "fresh", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Needs: []string{"held"}}}}
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
	for _, node := range snapshot.Nodes {
		if node.OwnerExecution == nil || !node.OwnerExecution.Allowed {
			t.Fatalf("active owner's placement was denied: %+v", node.OwnerExecution)
		}
	}
	ref := runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "held"}
	held, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: ref, Actor: actor, Policy: runstate.SourceNone})
	if err != nil || held.Kind != runstate.Execute || held.Claim == nil {
		t.Fatalf("active self claim: %+v %v", held, err)
	}
	holder := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: held.Claim.CapabilityPrefix}, CapabilityPrefix: held.Claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: held.Claim.Holder, Generation: held.Claim.Generation}}
	permission, err := tenant.MayStart(ctx, runstate.CapacityBinding{RunID: admitted.RunID, NodeID: string(ref.NodeID), Generation: int64(held.Claim.Generation)}, holder)
	if err != nil || permission.Kind != runstate.AcquireGranted {
		t.Fatalf("held admission: %+v %v", permission, err)
	}
	if _, err := tenant.TransitionNode(ctx, StartNode{Ref: ref, Actor: holder, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().ActivateLocalRunner(ctx, localRunnerSubject, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().RenewUnit(ctx, owner.Award.CapabilityPrefix, false); err != nil {
		t.Fatal("draining owner renewal:", err)
	}
	if _, err := s.AsOperator().RenewUnit(ctx, held.Claim.CapabilityPrefix, false); err != nil {
		t.Fatal("draining held job renewal:", err)
	}
	if _, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, admitted.RunID); err != nil {
		t.Fatal("draining owner cannot await its held job:", err)
	}
	child := in
	child.Authority, child.IdempotencyKey = holder.Authority, "held-child"
	child.Request.Pipeline = "child"
	child.Request.Lineage = runspec.Lineage{ParentRunID: admitted.RunID, ParentJobID: string(ref.NodeID)}
	if _, err := tenant.AdmitRun(ctx, child); err != nil {
		t.Fatal("draining job cannot admit a child:", err)
	}
	if _, err := tenant.TransitionNode(ctx, ReportNode{Ref: ref, Actor: holder, Outcome: runstate.NodeSuccess, SourceAttempt: sourceAttempt(1)}); err != nil {
		t.Fatal("draining held job report:", err)
	}
	snapshot, err = tenant.ReadOwnerSnapshot(ctx, actor.Authority, admitted.RunID)
	if err != nil {
		t.Fatal("draining owner observation:", err)
	}
	for _, node := range snapshot.Nodes {
		if node.OwnerExecution != nil && node.OwnerExecution.Allowed {
			t.Errorf("draining owner can self-claim %s", node.Ref.NodeID)
		}
	}
	fresh := runspec.NodeRef{RunID: ref.RunID, NodeID: "fresh"}
	miss, err := tenant.ReconcileNode(ctx, ReconcileRequest{Ref: fresh, Actor: actor, Policy: runstate.SourceNone})
	if err != nil || miss.Kind != runstate.Dispatch || miss.Claim != nil || miss.Node.ReadyAt == nil {
		t.Fatalf("draining owner must dispatch fresh work: %+v %v", miss, err)
	}
	claimed, err := s.AsOperator().ClaimUnit(ctx, UnitDemand{CapabilityPrefix: second.CapabilityPrefix, Target: &UnitRef{Kind: capability.UnitNode, RunID: fresh.RunID, NodeID: fresh.NodeID}})
	if err != nil || claimed.Award.CapabilityPrefix == "" {
		t.Fatalf("successor fresh claim: %+v %v", claimed, err)
	}
	if _, err := tenant.ReadOwnerSnapshot(ctx, actor.Authority, admitted.RunID); err != nil {
		t.Fatal("draining owner cannot await successor:", err)
	}
}

func TestLocalRunnerClaimsUseNearestRegisteredAncestor(t *testing.T) {
	s := localRunnerStore(t)
	first := registerLocalRunner(t, s, "first")
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, first.ID); err != nil {
		t.Fatal(err)
	}
	delegate := func(parent string) string {
		t.Helper()
		issued, err := s.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{
			Parent: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent}, Audience: capability.Controller,
			Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone},
		})
		if err != nil {
			t.Fatal(err)
		}
		return issued.Capability.Prefix
	}
	input := first
	input.ID, input.Machine, input.State, input.CapabilityPrefix = "second", "", "", delegate(first.CapabilityPrefix)
	second, err := s.AsOperator().RegisterLocalRunner(t.Context(), localRunnerSubject, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().ActivateLocalRunner(t.Context(), localRunnerSubject, second.ID); err != nil {
		t.Fatal(err)
	}
	descendant := delegate(second.CapabilityPrefix)
	demand := localRunnerTrigger(t, s, "first-work", 1)
	demand.CapabilityPrefix = descendant
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
		t.Fatal("nearest active runner lost to its draining ancestor:", err)
	}
	if err := s.AsOperator().DrainLocalRunner(t.Context(), localRunnerSubject, second.ID); err != nil {
		t.Fatal(err)
	}
	demand = localRunnerTrigger(t, s, "next-work", 2)
	demand.CapabilityPrefix = descendant
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draining registered ancestor admitted a descendant: %v", err)
	}
}
