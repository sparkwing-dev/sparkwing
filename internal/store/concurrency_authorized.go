package store

import (
	"context"
	"database/sql"
	"reflect"
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
	if a.Target.Team != string(t.team) || a.Target.SlotKey == "" || a.ResultReplay || a.LogTailAttempt != 0 {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	chain, err := authorityChainTx(ctx, tx, a.Principal.Authority, true)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	now := tx.now(ctx)
	snapshot := capability.AuthenticationSnapshot{Now: now}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal})
	}
	snapshot.Binding, err = authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	route, ok := capability.LookupRoute("GET", "/api/v1/queue/groups/{group}")
	if !ok {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	auth, err := capability.AuthenticateSnapshot(snapshot, route, capability.RequestEvidence{Target: a.Target})
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	if !reflect.DeepEqual(auth.Principal.Capability.Binding, a.Binding) {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	var live []capability.LiveAccess
	for _, option := range auth.Live {
		if reflect.DeepEqual(route.Access[option.RuleIndex], a.Access) {
			live = append(live, option)
		}
	}
	auth.Live = live
	current, err := capability.Authorize(ctx, tx, route, auth, a.Target)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	until := snapshot.Binding.LeaseExpiresAt
	if auth.Principal.Capability.ExpiresAt != nil && (until.IsZero() || auth.Principal.Capability.ExpiresAt.Before(until)) {
		until = *auth.Principal.Capability.ExpiresAt
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
