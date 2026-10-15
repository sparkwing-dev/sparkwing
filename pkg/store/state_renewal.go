package store

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// RenewNodeClaim extends only a live exact claim and its continuously retained capacity.
func (t *Tenant) RenewNodeClaim(ctx context.Context, ref NodeRef, actor Actor) (LeaseBeat, error) {
	var beat LeaseBeat
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		return t.renewNodeClaimTx(ctx, tx, ref, actor, &beat)
	})
	return beat, err
}

func (t *Tenant) renewNodeClaimTx(ctx context.Context, tx *storeTx, ref NodeRef, actor Actor, beat *LeaseBeat) error {
	now := tx.now()
	if err := prepareStateActorTx(ctx, tx, t.team, ref.RunID, actor, now); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, t.team, ref.RunID); err != nil {
		return err
	}
	row, err := readNodeTx(ctx, tx, t.team, ref)
	if err != nil {
		return err
	}
	now = tx.now()
	if err = checkHolderTx(ctx, tx, t.team, row, actor, now); err != nil {
		return err
	}
	expiry, err := boundedClaimExpiryTx(ctx, tx, actor, now)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE nodes SET lease_expires_at=?,last_heartbeat=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND claimed_by=?`, expiry.UnixNano(), now.UnixNano(), t.team, ref.RunID, ref.NodeID, actor.Generation, actor.Holder); err != nil {
		return err
	}
	if err = renewCapacityTx(ctx, tx, t.team, ref.RunID, ref.NodeID, now, expiry); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, t.team, ref.RunID)
	if err != nil {
		return err
	}
	*beat = LeaseBeat{ExpiresAt: expiry, CancelRequested: row.CancelRequestedAt != nil || run.CancelRequestedAt != nil, CancelReason: run.CancelReason}
	if row.CancelRequestedAt != nil {
		beat.CancelReason = row.CancelReason
	}
	unit := UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}
	if !beat.CancelRequested {
		if err := computeUnitBillingTx(ctx, tx, t.team, unit, row.Generation, row.Attempt, true); err != nil {
			return err
		}
	}
	return accountUnitBillingTx(ctx, tx, t.team, unit, row.Generation, tx.now(), false)
}

// RenewOwner renews owner-bound jobs and capacity within the owner lease transaction.
func (t *Tenant) RenewOwner(ctx context.Context, actor Actor) (LeaseBeat, error) {
	var beat LeaseBeat
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		return t.renewOwnerTx(ctx, tx, actor, &beat)
	})
	return beat, err
}

func (t *Tenant) renewOwnerTx(ctx context.Context, tx *storeTx, actor Actor, beat *LeaseBeat) error {
	runID := actor.Owner.RunID
	now := tx.now()
	if runID == "" {
		return errors.New("owner renewal requires a run")
	}
	if err := prepareStateActorTx(ctx, tx, t.team, runID, actor, now); err != nil {
		return err
	}
	if err := checkOwnerTx(ctx, tx, t.team, actor, runID, now); err != nil {
		return err
	}
	if err := lockStateRunTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	run, err := readRunTx(ctx, tx, t.team, runID)
	if err != nil {
		return err
	}
	now = tx.now()
	if err := checkOwnerTx(ctx, tx, t.team, actor, runID, now); err != nil {
		return err
	}
	if run.FinishedAt != nil {
		return holderRefusal()
	}
	expiry := now.Add(limits.DefaultLease)
	ownerCancelled := false
	if actor.Owner.Kind == OwnerController {
		var until int64
		if err = tx.QueryRowContext(ctx, `SELECT until FROM leases WHERE name=?`, actor.Owner.Name).Scan(&until); err != nil {
			return err
		}
		expiry = time.Unix(0, until).UTC()
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT cancel_requested_at IS NOT NULL FROM triggers WHERE team=? AND id=?`, t.team, runID).Scan(&ownerCancelled); err != nil {
			return err
		}
		expiry, err = boundedClaimExpiryTx(ctx, tx, actor, now)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=? AND generation=? AND claimed_by=?`, expiry.UnixNano(), t.team, runID, actor.Owner.Generation, actor.Owner.Holder); err != nil {
			return err
		}
	}
	// safety: awards parent to the claimant root; only the persisted self-claim fence identifies jobs this owner may renew.
	_, err = tx.ExecContext(ctx, `UPDATE nodes SET lease_expires_at=CASE
 WHEN (SELECT expires_at FROM capabilities WHERE prefix=nodes.capability_prefix)<?
 THEN (SELECT expires_at FROM capabilities WHERE prefix=nodes.capability_prefix) ELSE ? END,last_heartbeat=?
 WHERE team=? AND run_id=? AND status<>'done' AND claimed_by=? AND lease_expires_at>? AND owner_capability_prefix=?
 AND capability_prefix IN (SELECT prefix FROM capabilities WHERE team=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?))`, expiry.UnixNano(), expiry.UnixNano(), now.UnixNano(), t.team, runID, actor.Owner.Holder, now.UnixNano(), actor.CapabilityPrefix, t.team, now.UnixNano())
	if err != nil {
		return err
	}
	if err = renewCapacityTx(ctx, tx, t.team, runID, "", now, expiry); err != nil {
		return err
	}
	*beat = LeaseBeat{ExpiresAt: expiry, CancelRequested: run.CancelRequestedAt != nil || ownerCancelled, CancelReason: run.CancelReason}
	if err := chargeOwnedNodesBillingTx(ctx, tx, t.team, actor, now); err != nil {
		return err
	}
	if actor.Owner.Kind == OwnerTrigger {
		return accountOwnerBillingTx(ctx, tx, t.team, runID, actor.Owner.Generation, now, false)
	}
	return nil
}

func boundedClaimExpiryTx(ctx context.Context, tx *storeTx, actor Actor, now time.Time) (time.Time, error) {
	c, err := loadCapabilityTx(ctx, tx, actor.CapabilityPrefix)
	if err != nil {
		return time.Time{}, err
	}
	expiry := now.Add(limits.DefaultLease)
	for _, bound := range []*time.Time{c.ExpiresAt, c.Binding.Deadline} {
		if bound != nil && bound.Before(expiry) {
			expiry = *bound
		}
	}
	if !expiry.After(now) {
		return time.Time{}, holderRefusal()
	}
	return expiry, nil
}
