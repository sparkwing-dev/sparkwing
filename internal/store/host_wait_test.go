package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestRunViewExplainsLocalJobItsHostCannotExecute(t *testing.T) {
	ctx := t.Context()
	tenant, in, _ := intakeFixture(t)
	s := tenant.s
	root := authorityRoot("local-owner", "a")
	root.Subject.Kind = capability.SubjectLocal
	root.Grants = []capability.Grant{capability.RunsWrite, capability.NodesClaim, capability.TriggersClaim}
	authorityPersist(t, s, root)
	s.claimProfiles = claimProfileFake{profile: match.Profile{Labels: []string{"host=laptop"}}}
	in.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}
	in.Request.Origin, in.Request.Dispatch = runspec.OriginLocal, runspec.DispatchHost
	in.Request.Git.Repository.Key = "git:example.com/repo"
	admitted, err := tenant.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	award, err := s.AsOperator().ClaimUnit(ctx, UnitDemand{CapabilityPrefix: root.Prefix, Labels: []string{"host=laptop"}, Target: &UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Award.CapabilityPrefix}, CapabilityPrefix: award.Award.CapabilityPrefix, Owner: award.Owner}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "here", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}},
		{ID: "elsewhere", Inline: true, ResultStep: "run", Steps: []nodeproto.StepSpec{{ID: "run"}}, Requires: []string{"host=moonborn"}},
	}}
	declarations := runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: declarations, Plan: plan, Context: settings.Context{Local: true}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(ctx, PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: map[string]string{"message": "hello"}, SecretArgs: []string{}, Settings: resolved, Guards: runspec.Guards{Passed: true}}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []runspec.NodeID{"here", "elsewhere"} {
		if _, err := tenant.TransitionNode(ctx, ReadyNode{Ref: runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: id}, Actor: actor}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := tenant.ReadRunView(ctx, admitted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reason := view.HostIneligible["elsewhere"]
	if _, explained := view.HostIneligible["here"]; explained || !strings.Contains(reason, "requires runner labels host=moonborn") || !strings.Contains(reason, "host=laptop") {
		t.Fatalf("host ineligibility = %+v", view.HostIneligible)
	}
}
