package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ClaimProfileResolver supplies enrolled placement facts and current host capacity.
// Implementations bind results to the authenticated principal and its live root;
// granted identity, pools and repository authority come from persisted registration
// and capability facts, never from claim labels or process environment.
// ResolveClaimProfile runs inside a store transaction and must not mutate store state.
// The returned Resources are current availability, distinct from Profile.Capacity.
type ClaimProfileResolver interface {
	ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error)
}

// SetClaimProfileResolver installs enrollment and self-claim observations before concurrent use.
// The resolver uses existing authority records and registration facts; no second
// credential store or caller-selected identity is introduced by this boundary.
func (s *Store) SetClaimProfileResolver(resolver ClaimProfileResolver) {
	panic("unimplemented: c1-placement")
}
