package store

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type hydrationClaimProfiles struct {
	observerCalls int
	foreignCalls  int
}

func (p *hydrationClaimProfiles) ResolveClaimProfile(_ context.Context, principal capability.Principal) (match.Profile, error) {
	if principal.Capability.Prefix == "observer" {
		p.observerCalls++
	}
	if principal.Capability.Prefix == "foreign-observer" {
		p.foreignCalls++
	}
	return match.Profile{}, nil
}

func TestNamedClaimsHydrateObserversOnlyForPlacement(t *testing.T) {
	for _, ranked := range []bool{false, true} {
		name := "pinned"
		if ranked {
			name = "ranked"
		}
		t.Run(name, func(t *testing.T) {
			s, demand := unitClaimFixture(t)
			observer := authorityRoot("observer", "a")
			authorityPersist(t, s, observer)
			if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: observer.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: demand.Labels}); err != nil {
				t.Fatal(err)
			}
			request := runspec.Request{Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController}
			if !ranked {
				request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerName, Value: "runner"}}}
			}
			sealed := sealWith(t, runspec.SealedRequest{Request: request}, assertedProvenance(t, "ci", "", ""))
			rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?`, sealed)
			profiles := &hydrationClaimProfiles{}
			s.claimProfiles = profiles
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			if claim.RunID != "run" || claim.NodeID != "job" {
				t.Fatalf("claim = %+v", claim)
			}
			if got := profiles.observerCalls > 0; got != ranked {
				t.Fatalf("observer profile resolved %d times; placement needs observers=%t", profiles.observerCalls, ranked)
			}
		})
	}
}

func TestTeamClaimsDoNotHydrateOtherTeamsObservedMachines(t *testing.T) {
	s, demand := unitClaimFixture(t)
	foreign := authorityRoot("foreign-observer", "b")
	authorityPersist(t, s, foreign)
	if err := s.AsOperator().RecordRunnerPoll(t.Context(), UnitDemand{CapabilityPrefix: foreign.Prefix, Kinds: []capability.UnitKind{capability.UnitNode}, Labels: demand.Labels}); err != nil {
		t.Fatal(err)
	}
	request := runspec.Request{Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchController}
	sealed := sealWith(t, runspec.SealedRequest{Request: request}, assertedProvenance(t, "ci", "", ""))
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=?`, sealed)
	profiles := &hydrationClaimProfiles{}
	s.claimProfiles = profiles
	if _, err := s.AsOperator().ClaimUnit(t.Context(), demand); err != nil {
		t.Fatal(err)
	}
	if profiles.foreignCalls != 0 {
		t.Fatalf("claim hydrated another team's machine %d times", profiles.foreignCalls)
	}
}
