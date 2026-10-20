package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// CloudAwardResources reads Cloud's committed allocation or a self-hosted pool's sealed resource pins.
func (o *Operator) CloudAwardResources(ctx context.Context, ref capability.AuthorityRef) (*match.Resources, error) {
	tx, err := o.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	resources, err := cloudAwardResourcesTx(ctx, tx, ref)
	if err != nil {
		return nil, err
	}
	return resources, tx.Commit()
}

func cloudAwardResourcesTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) (*match.Resources, error) {
	chain, err := authorityChainTx(ctx, tx, ref, false)
	if err != nil {
		return nil, err
	}
	c := chain[0].principal.Capability
	if len(chain) > 1 && launcherClaimant(chain[len(chain)-1].principal.Capability) && !capability.CloudLauncher(chain[len(chain)-1].principal.Capability) {
		run, err := readRunTx(ctx, tx, Team(c.Team), c.Binding.RunID)
		if err != nil {
			return nil, err
		}
		resources, err := unitResourcesTx(ctx, tx, Team(c.Team), run, UnitRef{Kind: c.Unit, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID})
		if err != nil {
			return nil, err
		}
		allocation := matchingResources(resources)
		return &allocation, nil
	}
	if c.Billing == nil || c.Billing.ExecutorClass != "cloud" {
		return nil, nil
	}
	if !c.Billing.Metered {
		return nil, authorityError(apierr.IntegrityFailure, "Cloud award has no metered allocation")
	}
	award, err := readBillingAwardTx(ctx, tx, Team(c.Team), c.Billing.ReservationID)
	if errors.Is(err, ErrNotFound) {
		err = authorityError(apierr.IntegrityFailure, "Cloud award has no billing record")
	}
	if err != nil {
		return nil, err
	}
	generation := c.Binding.Generation
	if c.Binding.Expansion != nil {
		generation = c.Binding.Expansion.Generation
	}
	if award.provenance != *c.Billing || award.run != c.Binding.RunID || award.node != c.Binding.NodeID || award.unit != c.Unit || award.generation != generation {
		return nil, authorityError(apierr.IntegrityFailure, "Cloud allocation differs from its award")
	}
	_, class, _, err := billingPriceTx(ctx, tx, award)
	if err != nil {
		return nil, err
	}
	memory := CPUClassMemoryBytes(class)
	if class <= 0 || memory <= 0 {
		return nil, authorityError(apierr.IntegrityFailure, "Cloud allocation has no priced resources")
	}
	return &match.Resources{Cores: float64(class), MemoryBytes: memory}, nil
}
