package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestOwnerCannotForgetExpiredSelfClaim(t *testing.T) {
	s, demand, now := meteredUnitFixture(t, false)
	s.SetClaimProfileResolver(claimProfileFake{profile: match.Profile{Capacity: match.Resources{Cores: 8}}, available: match.Resources{Cores: 8}})
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	owner, actor := meteredUnitClaim(t, s, demand)
	first, err := s.RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true)
	if err != nil {
		t.Fatal(err)
	}
	tenant := &Tenant{s: s, team: "a"}
	self, err := tenant.ReconcileNode(t.Context(), ReconcileRequest{Ref: NodeRef{"run", "job"}, Actor: actor, Policy: SourceNone})
	if err != nil || self.Kind != Execute {
		t.Fatalf("self claim: %+v %v", self, err)
	}
	p, err := s.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: self.Claim.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	_, holder, err := renewalActor(p.Capability)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: self.Node.Ref, Actor: holder, To: NodeRunning, Cause: CauseStart, SourceAttempt: sourceAttempt(0)}); err != nil {
		t.Fatal(err)
	}
	*now = self.Claim.LeaseExpiresAt.Add(5 * time.Second)
	if !first.ExpiresAt.After(*now) {
		t.Fatalf("fixture owner lease %v must outlive child %v", first.ExpiresAt, self.Claim.LeaseExpiresAt)
	}
	// An unavailable controller can recover while the owner keeper is live but its self-claimed job has lapsed.
	beat, err := s.RenewUnit(t.Context(), owner.Award.CapabilityPrefix, true)
	var refusal *ErrIllegalTransition
	if !errors.As(err, &refusal) || refusal.Reason != ReasonHolderRights {
		t.Fatalf("owner renewed to %v after its executing job expired at %v; want holder refusal, got %v", beat.ExpiresAt, self.Claim.LeaseExpiresAt, err)
	}
}
