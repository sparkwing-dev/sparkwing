package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestFirstSuccessfulRunSurvivesAuditRetentionSQLite(t *testing.T) {
	assertFirstSuccessfulRunSurvivesAuditRetention(t, storetest.NewSQLite(t).Open(t, store.WithPolicies(sealStorePolicies)))
}

func TestFirstSuccessfulRunSurvivesAuditRetentionPostgres(t *testing.T) {
	assertFirstSuccessfulRunSurvivesAuditRetention(t, storetest.NewPostgres(t).Open(t, store.WithPolicies(sealStorePolicies)))
}

func assertFirstSuccessfulRunSurvivesAuditRetention(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := t.Context()
	tenant, first := runCompletionFixture(t, s)
	run, err := tenant.ReadRun(ctx, string(first.Unit.RunID))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.AsOperator().ResolveAuthority(ctx, first.Transition.Actor.Authority)
	if err != nil || owner.Capability.Parent == nil {
		t.Fatalf("owner ancestry: %+v, %v", owner, err)
	}
	if _, err := tenant.FinishRunAndUnit(ctx, first); err != nil {
		t.Fatal(err)
	}
	if events, err := tenant.BusinessEvents(ctx, store.BusinessEventFirstRunSucceeded, 10, ""); err != nil || len(events) != 1 {
		t.Fatalf("first-success events before retention: %+v, %v", events, err)
	}
	ageAuditEvents(t, s, store.BusinessEventFirstRunSucceeded, time.Now().Add(-401*24*time.Hour))
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "maintenance", "first-success-retention", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Lease: lease, Limit: 100})
	if err != nil || result.AuditEvents != 1 {
		t.Fatalf("prune first-success audit: %+v, %v", result, err)
	}
	admitted, err := tenant.AdmitRun(ctx, store.IntakeRequest{Authority: run.Request.AuthorityRef, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: string(first.Unit.RunID)}}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.AsOperator().ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: owner.Capability.Parent.ID, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: runspec.RunID(admitted.RunID)}})
	if err != nil {
		t.Fatal(err)
	}
	actor := runstate.Actor{Kind: runstate.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	if run.Spec == nil {
		t.Fatal("first run has no accepted plan")
	}
	if _, err := tenant.AcceptPlan(ctx, store.PlanAcceptance{RunID: admitted.RunID, Actor: actor, Seal: runspec.SealInput{Request: admitted.Request, Declarations: run.Spec.Declarations, Resolved: run.Spec.Resolved, Plan: run.Plan}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(ctx, runstate.RunTransition{RunID: runspec.RunID(admitted.RunID), Actor: actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Unit: claim.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(admitted.RunID), Actor: actor, Cause: runstate.RunFinish}}); err != nil {
		t.Fatal(err)
	}
	second, err := tenant.ReadRun(ctx, admitted.RunID)
	if err != nil || second.State != runstate.RunSuccess || second.FinishedAt == nil {
		t.Fatalf("later run did not succeed: %+v, %v", second, err)
	}
	if events, err := tenant.BusinessEvents(ctx, store.BusinessEventFirstRunSucceeded, 10, ""); err != nil || len(events) != 0 {
		t.Fatalf("later success became first after audit expiry: %+v, %v", events, err)
	}
}
