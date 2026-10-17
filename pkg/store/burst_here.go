package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func hereObserverTx(ctx context.Context, tx *storeTx, p capability.Principal) (bool, error) {
	c := p.Capability
	if c.Binding.RunID == "" || c.Binding.Kind != capability.BindingTrigger {
		return false, nil
	}
	chain, err := authorityChainTx(ctx, tx, p.Authority, false)
	if err != nil {
		return false, err
	}
	here := false
	for _, ancestor := range chain {
		a := ancestor.principal.Capability
		here = here || a.Purpose == capability.PurposeHere && a.Binding.RunID == c.Binding.RunID
	}
	if !here {
		return false, nil
	}
	if c.Purpose == capability.PurposeHere {
		return true, nil
	}
	var prefix string
	err = tx.QueryRowContext(ctx, `SELECT capability_prefix FROM triggers WHERE team=? AND id=? AND generation=? AND claimed_by=? AND lease_expires_at>?`, c.Team, c.Binding.RunID, c.Binding.Generation, c.Binding.Holder, tx.now().UnixNano()).Scan(&prefix)
	if err == sql.ErrNoRows {
		return false, holderRefusal()
	}
	if err != nil {
		return false, err
	}
	if prefix != c.Prefix {
		return false, holderRefusal()
	}
	return true, nil
}

// RecordHereAvailability updates only the authenticated here owner's fenced host observation.
func (s *Store) RecordHereAvailability(ctx context.Context, authority capability.AuthorityRef, available match.Resources) error {
	var demand UnitDemand
	err := s.inTx(ctx, func(tx *storeTx) error {
		p, err := tx.ResolveAuthority(ctx, authority)
		if err != nil {
			return err
		}
		here, err := hereObserverTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if !here {
			return bindingRefusal()
		}
		demand, err = ownerClaimDemandTx(ctx, tx, p)
		if err != nil {
			return err
		}
		demand.CapabilityPrefix = p.Capability.Prefix
		demand.Available = available
		if !validMatchingResources(available) || available.Cores > demand.Capacity.Cores && demand.Capacity.Cores > 0 || available.MemoryBytes > demand.Capacity.MemoryBytes && demand.Capacity.MemoryBytes > 0 {
			return authorityError(apierr.BadRequest, "invalid here availability")
		}
		body, err := json.Marshal(runnerPoll{UnitDemand: demand, ObservedAt: tx.now()})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE capabilities SET claim_observation_json=?,last_used_at=? WHERE team=? AND prefix=?`, body, tx.now().UnixNano(), p.Capability.Team, p.Capability.Prefix)
		return err
	})
	return err
}
