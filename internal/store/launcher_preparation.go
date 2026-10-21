package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// LauncherPreparation reports the committed authority bound for an acknowledged bootstrap fence.
type LauncherPreparation struct {
	Fence     capability.LeaseRef
	ExpiresAt time.Time
}

// renewLauncherPreparation extends only live unexchanged bootstrap leases owned by this launcher.
func (s *Store) renewLauncherPreparation(ctx context.Context, authority capability.AuthorityRef, preparing []capability.LeaseRef) ([]LauncherPreparation, error) {
	if err := validateLauncherPreparationFences(preparing); err != nil {
		return nil, err
	}
	prepared := []LauncherPreparation{}
	err := s.inLauncherBatchTx(ctx, authority, preparing, true, func(tx *storeTx) error {
		prepared = prepared[:0]
		chain, err := authorityChainTx(ctx, tx, authority, true)
		if err != nil {
			return err
		}
		claimant := chain[0].principal.Capability
		if authority.Kind != capability.AuthorityCapability || claimant.Audience != capability.Controller || !launcherClaimant(claimant) {
			return bindingRefusal()
		}
		root := chain[len(chain)-1].principal
		for _, fence := range preparing {
			expiry, err := renewLauncherPreparationTx(ctx, tx, root, claimant, fence)
			if err != nil {
				return err
			}
			if !expiry.IsZero() {
				prepared = append(prepared, LauncherPreparation{Fence: fence, ExpiresAt: expiry})
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

func validateLauncherPreparationFences(preparing []capability.LeaseRef) error {
	seen := map[capability.LeaseRef]bool{}
	for _, fence := range preparing {
		if seen[fence] || fence.ID == "" || fence.Holder == "" || fence.Generation <= 0 || !slices.Contains([]capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}, fence.Kind) {
			return bindingRefusal()
		}
		seen[fence] = true
	}
	return nil
}

func renewLauncherPreparationTx(ctx context.Context, tx *storeTx, root capability.Principal, claimant capability.Capability, fence capability.LeaseRef) (time.Time, error) {
	if err := launcherReservationAwardTx(ctx, tx, root.Authority, claimant.Subject, fence); err != nil {
		return time.Time{}, err
	}
	row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: fence.Kind, UnitID: fence.ID})
	if err != nil {
		if endedLauncherPreparation(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if row.prefix == "" || row.holder != fence.Holder || row.generation != fence.Generation {
		return time.Time{}, nil
	}
	current, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.prefix})
	if err != nil {
		if endedLauncherPreparation(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	award, live, err := readLauncherAwardTx(ctx, tx, current.Capability.Prefix, claimant, root)
	if err != nil {
		return time.Time{}, err
	}
	if !live || award.Claim.Award.Holder != fence.Holder || int64(award.Claim.Award.Generation) != fence.Generation {
		return time.Time{}, nil
	}
	if current.Capability.Delivery == capability.DeliveryDirect && current.Capability.Issuance == capability.Exchange {
		return time.Time{}, nil
	}
	if current.Capability.Delivery != capability.DeliveryBootstrap || current.Capability.Issuance != capability.Award {
		return time.Time{}, bindingRefusal()
	}
	unitRef := UnitRef{Kind: award.Claim.Kind, RunID: runspec.RunID(award.Claim.RunID), NodeID: runspec.NodeID(award.Claim.NodeID), SourceGeneration: award.Claim.SourceGeneration}
	if err = lockStateRunTx(ctx, tx, award.Claim.Team, award.Claim.RunID); err != nil {
		return time.Time{}, err
	}
	beat := LeaseBeat{}
	funded, err := renewUnitBillingTx(ctx, tx, award.Claim.Team, unitRef, fence.Generation, &beat)
	if err != nil {
		return time.Time{}, err
	}
	expiry := tx.time
	if !beat.CancelRequested {
		if expiry, err = leaseBoundTx(ctx, tx, current.Capability.Prefix, tx.time, funded); err != nil {
			return time.Time{}, err
		}
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
		args = []any{expiry.UnixNano(), tx.time.UnixNano(), unit.Team, unit.RunID, unit.NodeID, current.Capability.Prefix, fence.Generation, fence.Holder}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return time.Time{}, err
	}
	if err = requireOneRow(result, "launcher preparation lease"); err != nil {
		return time.Time{}, err
	}
	if beat.CancelRequested {
		return time.Time{}, nil
	}
	if err = renewCapacityTx(ctx, tx, unit.Team, unit.RunID, unit.NodeID, tx.time, expiry); err != nil {
		return time.Time{}, err
	}
	return expiry, observeUnitTx(ctx, tx, current.Capability.Prefix)
}

func endedLauncherPreparation(err error) bool {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		return true
	}
	var failure *apierr.Error
	return errors.As(err, &failure) && (failure.Code == apierr.LockHeld || failure.Code == apierr.Unauthenticated || failure.Code == apierr.Forbidden)
}
