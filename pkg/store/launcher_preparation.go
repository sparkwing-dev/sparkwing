package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// RenewLauncherPreparation extends only live unexchanged bootstrap leases owned by this launcher.
func (s *Store) RenewLauncherPreparation(ctx context.Context, authority capability.AuthorityRef, preparing []capability.LeaseRef) ([]capability.LeaseRef, error) {
	prepared := []capability.LeaseRef{}
	err := s.inTx(ctx, func(tx *storeTx) error {
		prepared = prepared[:0]
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
		if err := lockLauncherBatchTx(ctx, tx, authority, preparing, true); err != nil {
			return err
		}
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
			return bindingRefusal()
		}
		root := chain[len(chain)-1].principal
		seen := map[capability.LeaseRef]bool{}
		for _, fence := range preparing {
			if seen[fence] || fence.ID == "" || fence.Holder == "" || fence.Generation <= 0 || !slices.Contains([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, fence.Kind) {
				return bindingRefusal()
			}
			seen[fence] = true
			renewed, err := renewLauncherPreparationTx(ctx, tx, root, claimant, fence)
			if err != nil {
				return err
			}
			if renewed {
				prepared = append(prepared, fence)
			}
		}
		_, err = authorityChainTx(ctx, tx, authority, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func renewLauncherPreparationTx(ctx context.Context, tx *storeTx, root capability.Principal, claimant capability.Capability, fence capability.LeaseRef) (bool, error) {
	if err := launcherReservationAwardTx(ctx, tx, root.Authority, claimant.Subject, fence); err != nil {
		return false, err
	}
	row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: fence.Kind, UnitID: fence.ID})
	if err != nil {
		if endedLauncherPreparation(err) {
			return false, nil
		}
		return false, err
	}
	if row.prefix == "" || row.holder != fence.Holder || row.generation != fence.Generation {
		return false, nil
	}
	current, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.prefix})
	if err != nil {
		if endedLauncherPreparation(err) {
			return false, nil
		}
		return false, err
	}
	award, live, err := readLauncherAwardTx(ctx, tx, current.Capability.Prefix, claimant, root)
	if err != nil {
		return false, err
	}
	if !live || award.Claim.Award.Holder != fence.Holder || award.Claim.Award.Generation != fence.Generation {
		return false, nil
	}
	if current.Capability.Delivery == capability.DeliveryDirect && current.Capability.Issuance == capability.Exchange {
		return false, nil
	}
	if current.Capability.Delivery != capability.DeliveryBootstrap || current.Capability.Issuance != capability.Award {
		return false, bindingRefusal()
	}
	unitRef := UnitRef{Kind: award.Claim.Kind, RunID: award.Claim.RunID, NodeID: award.Claim.NodeID, SourceGeneration: award.Claim.SourceGeneration}
	if err = computeUnitBillingTx(ctx, tx, award.Claim.Team, unitRef, fence.Generation, 0, true); err != nil {
		return false, err
	}
	if err = accountUnitBillingTx(ctx, tx, award.Claim.Team, unitRef, fence.Generation, tx.now(), false); err != nil {
		return false, err
	}
	beat := LeaseBeat{}
	if err = applyBillingCancellationTx(ctx, tx, award.Claim.Team, unitRef, fence.Generation, &beat); err != nil {
		return false, err
	}
	expiry, err := boundedClaimExpiryTx(ctx, tx, Actor{CapabilityPrefix: current.Capability.Prefix}, tx.now())
	if err != nil {
		return false, err
	}
	if beat.CancelRequested {
		expiry = tx.now()
	}
	unit := award.Claim
	var query string
	var args []any
	switch fence.Kind {
	case capability.UnitPlan:
		query = `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=? AND capability_prefix=? AND generation=? AND claimed_by=?`
		args = []any{expiry.UnixNano(), unit.Team, unit.RunID, current.Capability.Prefix, fence.Generation, fence.Holder}
	case capability.UnitEval:
		query = `UPDATE eval_units SET lease_expires_at=? WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=? AND capability_prefix=? AND generation=? AND claimed_by=?`
		args = []any{expiry.UnixNano(), unit.Team, unit.RunID, unit.NodeID, unit.SourceGeneration, current.Capability.Prefix, fence.Generation, fence.Holder}
	default:
		query = `UPDATE nodes SET lease_expires_at=?,last_heartbeat=? WHERE team=? AND run_id=? AND node_id=? AND capability_prefix=? AND generation=? AND claimed_by=?`
		args = []any{expiry.UnixNano(), tx.now().UnixNano(), unit.Team, unit.RunID, unit.NodeID, current.Capability.Prefix, fence.Generation, fence.Holder}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	if err = requireOneRow(result, "launcher preparation lease"); err != nil {
		return false, err
	}
	if beat.CancelRequested {
		return false, nil
	}
	if err = renewCapacityTx(ctx, tx, unit.Team, unit.RunID, unit.NodeID, tx.now(), expiry); err != nil {
		return false, err
	}
	return true, observeUnitTx(ctx, tx, current.Capability.Prefix)
}

func endedLauncherPreparation(err error) bool {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return true
	}
	var failure *apierr.Error
	return errors.As(err, &failure) && (failure.Code == apierr.LockHeld || failure.Code == apierr.Unauthenticated || failure.Code == apierr.Forbidden)
}
