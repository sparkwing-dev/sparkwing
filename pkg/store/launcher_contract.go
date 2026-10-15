package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// LauncherAward contains persisted execution inputs without reusable bearer material.
type LauncherAward struct {
	Claim                  UnitClaim
	Capability             capability.Capability
	Run                    RunRow
	Node                   NodeRow
	ParentCapabilityExpiry *time.Time
}

// ReadLauncherAwards reads current launcher-owned fences and execution inputs in one snapshot.
func (s *Store) ReadLauncherAwards(ctx context.Context, authority capability.AuthorityRef) ([]LauncherAward, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, authority, false)
	if err != nil {
		return nil, err
	}
	claimant := chain[0].principal.Capability
	if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
		return nil, bindingRefusal()
	}
	root := chain[len(chain)-1].principal
	prefixes, err := launcherPrefixesTx(ctx, tx, root.Authority, claimant.Subject.ID)
	if err != nil {
		return nil, err
	}
	out := []LauncherAward{}
	for _, prefix := range prefixes {
		award, live, err := readLauncherAwardTx(ctx, tx, prefix, claimant, root)
		if err != nil {
			return nil, err
		}
		if live {
			out = append(out, award)
		}
	}
	if _, err := authorityChainTx(ctx, tx, authority, false); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
