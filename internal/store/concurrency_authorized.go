package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func declaredSlotTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) (runspec.Concurrency, error) {
	b := c.Binding
	if b.Expansion != nil || b.RunID == "" || target.SlotRunID != b.RunID || target.SlotNodeID != b.NodeID || target.SlotGeneration != b.Generation || b.Generation <= 0 ||
		!(b.Kind == capability.BindingNode && c.Unit == capability.UnitNode || b.Kind == capability.BindingTrigger && c.Unit == capability.UnitTrigger || b.Kind == capability.BindingRun && c.Unit == capability.UnitPlan) {
		return runspec.Concurrency{}, bindingRefusal()
	}
	group, err := declaredCapacityGroupTx(ctx, tx, Team(c.Team), b.RunID, b.NodeID, target.SlotKey, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.Prefix})
	if err != nil {
		return runspec.Concurrency{}, err
	}
	if group.Scope == "box" {
		return runspec.Concurrency{}, bindingRefusal()
	}
	return group, nil
}

func bindingSlotTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	_, err := declaredSlotTx(ctx, tx, c, target)
	return err
}

func (t *Tenant) slotAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized) (capability.Authorized, time.Time, error) {
	if a.Target.SlotKey == "" || a.ResultReplay || a.LogTailAttempt != 0 {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	route, ok := capability.LookupRoute("GET", "/api/v1/queue/groups/{group}")
	if !ok {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	current, snapshot, err := t.reauthorizeTx(ctx, tx, a, route, true)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	until := snapshot.Binding.LeaseExpiresAt
	if current.Principal.Capability.ExpiresAt != nil && (until.IsZero() || current.Principal.Capability.ExpiresAt.Before(until)) {
		until = *current.Principal.Capability.ExpiresAt
	}
	if a.Access.Credential == capability.CredentialClaim {
		var deadline sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM triggers WHERE team=? AND id=?`, t.team, a.Binding.RunID).Scan(&deadline); err != nil {
			return capability.Authorized{}, time.Time{}, err
		}
		if deadline.Valid && (until.IsZero() || time.Unix(0, deadline.Int64).Before(until)) {
			until = time.Unix(0, deadline.Int64)
		}
		if !until.After(tx.now(ctx)) {
			return capability.Authorized{}, time.Time{}, holderRefusal()
		}
	}
	return current, until, nil
}

func slotLease(now, until time.Time, lease time.Duration) time.Duration {
	if lease <= 0 {
		lease = defaultConcurrencyLease
	}
	if remaining := until.Sub(now); remaining < lease {
		return remaining
	}
	return lease
}

// ReadAuthorizedGroup returns the selected tenant group under current read authority.
func (t *Tenant) ReadAuthorizedGroup(ctx context.Context, a capability.Authorized, key string) (*ConcurrencyState, error) {
	if key != a.Target.SlotKey {
		return nil, bindingRefusal()
	}
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	_, _, err = t.slotAuthorityTx(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	state, err := t.concurrencyStateTx(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	return state, tx.Commit()
}
