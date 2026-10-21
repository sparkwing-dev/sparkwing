package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// ClaimProfileResolver supplies enrolled placement facts.
// Implementations bind results to the authenticated principal and its live root;
// granted identity, pools and repository authority come from persisted registration
// and capability facts, never from claim labels or process environment.
// ResolveClaimProfile runs inside a store transaction and must not mutate store state.
type ClaimProfileResolver interface {
	ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error)
}

// ReadClaimProfile supplies controller claim observations from the same enrollment
// owner used by transactional matching. Claims re-read registration at award time.
func (o *Operator) ReadClaimProfile(ctx context.Context, authority capability.AuthorityRef) (match.Profile, error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return match.Profile{}, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, authority, false)
	if err != nil {
		return match.Profile{}, err
	}
	principal := chain[0].principal
	var profile match.Profile
	if o.s.claimProfiles != nil {
		profile, err = o.s.claimProfiles.ResolveClaimProfile(ctx, principal)
	} else {
		profile, err = enrolledClaimProfileTx(ctx, tx, chain)
	}
	if err != nil {
		return match.Profile{}, err
	}
	here := false
	for _, ancestor := range chain {
		here = here || ancestor.principal.Capability.Purpose == capability.PurposeHere
	}
	profile = authenticatedClaimProfile(principal.Capability, profile, here)
	return profile, tx.Commit()
}
