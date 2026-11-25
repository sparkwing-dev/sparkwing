package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestRelinquishedAwardStopsResolving(t *testing.T) {
	s, demand := burstFixture(t, runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "machines", Hold: 5 * time.Second}, {Target: "cloud"}}})
	owner := authorityMember(t, s, "owner", RoleOwner)
	demand.CapabilityPrefix = burstPersonalClaimant(t, s, owner.Authority, "laptop").Prefix
	fundUnitClaim(t, s)
	tenant := &Tenant{s: s, team: "a"}
	claim, actor := meteredUnitClaim(t, s, demand)
	award := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}
	resolve := func() error {
		return s.inTx(t.Context(), func(tx *storeTx) error {
			_, err := tx.ResolveAuthority(t.Context(), award)
			return err
		})
	}
	if err := resolve(); err != nil {
		t.Fatalf("live award did not resolve: %v", err)
	}
	if err := tenant.RelinquishStart(t.Context(), runstate.CapacityBinding{RunID: claim.RunID, NodeID: claim.NodeID, Generation: int64(claim.Award.Generation)}, actor); err != nil {
		t.Fatal(err)
	}
	requireUnauthenticated(t, resolve(), "relinquished award")
	next, _ := meteredUnitClaim(t, s, demand)
	if next.Award.CapabilityPrefix == award.ID || next.Award.Generation <= claim.Award.Generation {
		t.Fatalf("the job was not awarded again after its award was revoked: %+v", next.Award)
	}
}
