package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func declaredSlotTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) (runspec.Concurrency, error) {
	b := c.Binding
	if b.Expansion != nil || b.RunID == "" || target.SlotRunID != b.RunID || target.SlotNodeID != b.NodeID || target.SlotGeneration != b.Generation || b.Generation <= 0 ||
		!(b.Kind == capability.BindingNode && c.Unit == capability.UnitNode || b.Kind == capability.BindingTrigger && c.Unit == capability.UnitTrigger || b.Kind == capability.BindingRun && c.Unit == capability.UnitPlan) {
		return runspec.Concurrency{}, bindingRefusal()
	}
	scope, identity, name, ok := runspec.ParseConcurrencyKey(target.SlotKey)
	if !ok || scope == "run" && identity != b.RunID || scope == "box" {
		return runspec.Concurrency{}, bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), b.RunID)
	if err != nil {
		return runspec.Concurrency{}, err
	}
	if run.Spec == nil {
		return runspec.Concurrency{}, bindingRefusal()
	}
	groups := run.Spec.Resolved.Settings.Concurrency
	if b.NodeID != "" {
		row, err := readNodeTx(ctx, tx, Team(c.Team), NodeRef{b.RunID, b.NodeID})
		if err != nil {
			return runspec.Concurrency{}, err
		}
		groups = row.Execution.Concurrency
	}
	for _, group := range groups {
		if group.Group == name && group.Scope == scope {
			return group, nil
		}
	}
	return runspec.Concurrency{}, bindingRefusal()
}

func bindingSlotTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if _, err := declaredSlotTx(ctx, tx, c, target); err != nil {
		return err
	}
	for _, table := range []string{"concurrency_holders", "concurrency_waiters"} {
		rows, err := tx.QueryContext(ctx, `SELECT run_id,node_id,generation FROM `+table+` WHERE team=? AND key=? AND ((run_id=? AND node_id=?) OR (?<>'' AND holder_id=?))`, c.Team, target.SlotKey, target.SlotRunID, target.SlotNodeID, target.SlotHolderID, target.SlotHolderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var run, job string
			var generation int64
			if err := rows.Scan(&run, &job, &generation); err != nil {
				return errors.Join(err, rows.Close())
			}
			if run != target.SlotRunID || job != target.SlotNodeID || generation != target.SlotGeneration {
				_ = rows.Close()
				return bindingRefusal()
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tenant) slotAuthorityTx(ctx context.Context, tx *storeTx, a capability.Authorized, operation string) (capability.Authorized, time.Time, error) {
	if a.Target.Team != string(t.team) || a.Target.SlotKey == "" || a.ResultReplay || a.LogTailAttempt != 0 {
		return capability.Authorized{}, time.Time{}, bindingRefusal()
	}
	chain, err := authorityChainTx(ctx, tx, a.Principal.Authority, true)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	snapshot := capability.AuthenticationSnapshot{Now: tx.now()}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal, MembershipValid: true})
	}
	snapshot.Binding, err = authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.Authorized{}, time.Time{}, err
	}
	method := "POST"
	if operation == "" || operation == "/holder" || operation == "/resolve" {
		method = "GET"
	}
	route, ok := capability.LookupRoute(method, "/api/v1/queue/groups/{group}"+operation)
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
		if !until.After(tx.now()) {
			return capability.Authorized{}, time.Time{}, holderRefusal()
		}
	}
	return current, until, nil
}

// safety: slot mutations lock admission, controller lease, authority, owner unit, sorted runs/nodes, then group.
// Claims take authority before work too, so neither path can wait for authority while holding the other's work.
func authorizedConcurrency[T any](ctx context.Context, t *Tenant, a capability.Authorized, key, operation string, work func(*storeTx, capability.Authorized, time.Time) (T, error)) (T, error) {
	var zero T
	if key != a.Target.SlotKey {
		return zero, bindingRefusal()
	}
	readOnly := operation == "" || operation == "/holder"
	begin := t.s.beginTx
	if readOnly {
		begin = t.s.beginSnapshotReadTx
	}
	tx, err := begin(ctx)
	if err != nil {
		return zero, err
	}
	defer rollbackOrLog(tx)
	if !readOnly {
		if err := advisoryLockTx(ctx, tx, fmt.Sprintf("capacity-admission:%d:%s:%s", len(t.team), t.team, key)); err != nil {
			return zero, err
		}
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return zero, err
		}
	}
	current, until, err := t.slotAuthorityTx(ctx, tx, a, operation)
	if err != nil {
		return zero, err
	}
	if !readOnly {
		if a.Binding.RunID != "" {
			if err := lockClaimOwnerTx(ctx, tx, t.team, UnitRef{Kind: current.Principal.Capability.Unit, RunID: current.Binding.RunID}); err != nil {
				return zero, err
			}
		}
		if err := lockCapacityWorkTx(ctx, tx, t.team, key, NodeRef{a.Binding.RunID, a.Binding.NodeID}); err != nil {
			return zero, err
		}
		if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
			return zero, err
		}
	}
	result, err := work(tx, current, until)
	if err != nil {
		return zero, err
	}
	if _, _, err := t.slotAuthorityTx(ctx, tx, a, operation); err != nil {
		return zero, err
	}
	if readOnly {
		return result, tx.Commit()
	}
	if err := txCommitChecked(ctx, tx, tx.now().UnixNano(), concurrencyKey{t.team, key}); err != nil {
		return zero, err
	}
	return result, nil
}

func slotLease(now, until time.Time, lease time.Duration) time.Duration {
	if lease <= 0 {
		lease = DefaultConcurrencyLease
	}
	if remaining := until.Sub(now); remaining < lease {
		return remaining
	}
	return lease
}

// AcquireAuthorizedSlot admits only the current claim's accepted concurrency declaration.
func (t *Tenant) AcquireAuthorizedSlot(ctx context.Context, a capability.Authorized, req AcquireSlotRequest) (AcquireSlotResponse, error) {
	return authorizedConcurrency(ctx, t, a, req.Key, "/acquire", func(tx *storeTx, current capability.Authorized, until time.Time) (AcquireSlotResponse, error) {
		if req.RunID != current.Binding.RunID || req.NodeID != current.Binding.NodeID || req.HolderID == "" || req.HolderID != a.Target.SlotHolderID || req.CacheKeyHash != "" || req.CacheTTL != 0 {
			return AcquireSlotResponse{}, bindingRefusal()
		}
		group, err := declaredSlotTx(ctx, tx, current.Principal.Capability, a.Target)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		if req.Capacity != 0 && req.Capacity != group.Capacity || req.Cost != 0 && req.Cost != group.Cost || req.Policy != "" && req.Policy != group.OnLimit {
			return AcquireSlotResponse{}, bindingRefusal()
		}
		req.Capacity, req.Cost, req.Policy = group.Capacity, group.Cost, group.OnLimit
		req.CancelTimeout = 0
		if group.CancelTimeoutMS != nil {
			req.CancelTimeout = time.Duration(*group.CancelTimeoutMS) * time.Millisecond
		}
		response, err := t.acquireConcurrencySlotTx(ctx, tx, req, nil, until)
		if err != nil {
			return AcquireSlotResponse{}, err
		}
		if response.Kind == AcquireGranted || response.Kind == AcquireCancellingOthers {
			holder, err := t.authorizedHolderTx(ctx, tx, a, req.Key, req.HolderID)
			if err != nil {
				return AcquireSlotResponse{}, err
			}
			response.LeaseExpiresAt = holder.LeaseExpiresAt
		}
		actor := Actor{Kind: ActorHolder, Authority: current.Principal.Authority, CapabilityPrefix: current.Principal.Capability.Prefix, Holder: current.Binding.Holder, Generation: current.Binding.Generation}
		for _, id := range response.SupersededIDs {
			if err := cancelSupersededCapacityTx(ctx, tx, t.team, req.Key, id, actor); err != nil {
				return AcquireSlotResponse{}, err
			}
		}
		if err := slotReadEdgeTx(ctx, tx, current, response.LeaderRunID, response.LeaderNodeID); err != nil {
			return AcquireSlotResponse{}, err
		}
		return response, nil
	})
}

func slotReadEdgeTx(ctx context.Context, tx *storeTx, a capability.Authorized, run, job string) error {
	if run == "" && job == "" {
		return nil
	}
	if run == "" || job == "" {
		return bindingRefusal()
	}
	if a.Access.Credential != capability.CredentialClaim {
		if err := bindingRunExistsTx(ctx, tx, a.Target.Team, run); err != nil {
			return err
		}
		_, err := readNodeTx(ctx, tx, Team(a.Target.Team), NodeRef{run, job})
		if errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return bindingRefusal()
		}
		return err
	}
	return tx.CheckBinding(ctx, a.Principal, capability.BindReadSet, capability.Target{Team: a.Target.Team, RunID: run, NodeID: job})
}

// HeartbeatAuthorizedSlot renews only an unexpired holder within the current authority deadline.
func (t *Tenant) HeartbeatAuthorizedSlot(ctx context.Context, a capability.Authorized, key, id string, lease time.Duration) (time.Time, bool, error) {
	type renewal struct {
		expires    time.Time
		superseded bool
	}
	result, err := authorizedConcurrency(ctx, t, a, key, "/heartbeat", func(tx *storeTx, current capability.Authorized, until time.Time) (renewal, error) {
		holder, err := t.authorizedHolderTx(ctx, tx, a, key, id)
		if err != nil {
			return renewal{}, err
		}
		expires := tx.now().Add(slotLease(tx.now(), until, lease))
		_, err = tx.ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=? WHERE team=? AND key=? AND holder_id=?`, expires.UnixNano(), t.team, key, id)
		return renewal{expires, holder.Superseded}, err
	})
	return result.expires, result.superseded, err
}

func (t *Tenant) authorizedHolderTx(ctx context.Context, tx *storeTx, a capability.Authorized, key, id string) (ConcurrencyHolder, error) {
	if id == "" || id != a.Target.SlotHolderID {
		return ConcurrencyHolder{}, bindingRefusal()
	}
	holder, err := scanHolder(tx.QueryRowContext(ctx, `SELECT `+holderColumns+` FROM concurrency_holders WHERE team=? AND key=? AND holder_id=?`, t.team, key, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ConcurrencyHolder{}, ErrLockHeld
	}
	if err != nil {
		return ConcurrencyHolder{}, err
	}
	if !holder.LeaseExpiresAt.After(tx.now()) {
		return ConcurrencyHolder{}, ErrLockHeld
	}
	if holder.RunID != a.Binding.RunID || holder.NodeID != a.Binding.NodeID {
		return ConcurrencyHolder{}, bindingRefusal()
	}
	return holder, nil
}

// ReadAuthorizedHolder returns only the current claim's live capacity holder.
func (t *Tenant) ReadAuthorizedHolder(ctx context.Context, a capability.Authorized, key, id string) (ConcurrencyHolder, error) {
	return authorizedConcurrency(ctx, t, a, key, "/holder", func(tx *storeTx, _ capability.Authorized, _ time.Time) (ConcurrencyHolder, error) {
		return t.authorizedHolderTx(ctx, tx, a, key, id)
	})
}

// ReleaseAuthorizedSlot releases, notifies and promotes under current reporting authority.
func (t *Tenant) ReleaseAuthorizedSlot(ctx context.Context, a capability.Authorized, key, id, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error {
	_, err := authorizedConcurrency(ctx, t, a, key, "/release", func(tx *storeTx, _ capability.Authorized, _ time.Time) (bool, error) {
		if outputRef != "" || cacheKeyHash != "" || ttl != 0 {
			return false, bindingRefusal()
		}
		if id == "" || id != a.Target.SlotHolderID {
			return false, bindingRefusal()
		}
		released, _, _, err := txReleaseHolder(ctx, tx, t.team, key, id)
		if err != nil {
			return false, err
		}
		_, err = txPromoteWaiters(ctx, tx, t.team, key, tx.now().UnixNano(), tx.now().Add(DefaultConcurrencyLease).UnixNano())
		return released, err
	})
	return err
}

// ReadAuthorizedGroup returns the selected tenant group under current read authority.
func (t *Tenant) ReadAuthorizedGroup(ctx context.Context, a capability.Authorized, key string) (*ConcurrencyState, error) {
	return authorizedConcurrency(ctx, t, a, key, "", func(tx *storeTx, current capability.Authorized, _ time.Time) (*ConcurrencyState, error) {
		state, err := t.concurrencyStateTx(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		for i := range state.Waiters {
			waiter := &state.Waiters[i]
			if current.Access.Credential == capability.CredentialClaim {
				waiter.CacheKeyHash = ""
			}
			if err := slotReadEdgeTx(ctx, tx, current, waiter.LeaderRunID, waiter.LeaderNodeID); err != nil {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.Forbidden {
					return nil, err
				}
				waiter.LeaderRunID, waiter.LeaderNodeID = "", ""
			}
		}
		return state, nil
	})
}

// ResolveAuthorizedWaiter checks declared leader edges before resolving the bound work.
func (t *Tenant) ResolveAuthorizedWaiter(ctx context.Context, a capability.Authorized, key, run, job, cacheKeyHash, leaderRun, leaderJob string, bypass bool) (WaiterResolution, error) {
	return authorizedConcurrency(ctx, t, a, key, "/resolve", func(tx *storeTx, current capability.Authorized, until time.Time) (WaiterResolution, error) {
		if run != current.Binding.RunID || job != current.Binding.NodeID || cacheKeyHash != "" {
			return WaiterResolution{}, bindingRefusal()
		}
		if err := slotReadEdgeTx(ctx, tx, current, leaderRun, leaderJob); err != nil {
			return WaiterResolution{}, err
		}
		var storedRun, storedJob string
		err := tx.QueryRowContext(ctx, `SELECT leader_run_id,leader_node_id FROM concurrency_waiters WHERE team=? AND key=? AND run_id=? AND node_id=?`, t.team, key, run, job).Scan(&storedRun, &storedJob)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WaiterResolution{}, err
		}
		if err == nil {
			if err := slotReadEdgeTx(ctx, tx, current, storedRun, storedJob); err != nil {
				return WaiterResolution{}, err
			}
		}
		response, err := t.resolveWaiterTx(ctx, tx, key, run, job, leaderRun, leaderJob, slotLease(tx.now(), until, DefaultConcurrencyLease), nil)
		if err != nil {
			return WaiterResolution{}, err
		}
		if response.Status == WaiterPromoted {
			var expires int64
			if err := tx.QueryRowContext(ctx, `SELECT lease_expires_at FROM concurrency_holders WHERE team=? AND key=? AND holder_id=?`, t.team, key, response.HolderID).Scan(&expires); err != nil {
				return WaiterResolution{}, err
			}
			if until.Before(time.Unix(0, expires)) {
				if _, err := tx.ExecContext(ctx, `UPDATE concurrency_holders SET lease_expires_at=? WHERE team=? AND key=? AND holder_id=?`, until.UnixNano(), t.team, key, response.HolderID); err != nil {
					return WaiterResolution{}, err
				}
				expires = until.UnixNano()
			}
			response.HolderLeaseExpires = time.Unix(0, expires)
		}
		return response, nil
	})
}

// CancelAuthorizedWaiter removes only the current claim's waiter or promoted holder.
func (t *Tenant) CancelAuthorizedWaiter(ctx context.Context, a capability.Authorized, key string, ref NodeRef) (bool, error) {
	return authorizedConcurrency(ctx, t, a, key, "/cancel-waiter", func(tx *storeTx, current capability.Authorized, _ time.Time) (bool, error) {
		if ref.RunID != current.Binding.RunID || ref.NodeID != current.Binding.NodeID {
			return false, bindingRefusal()
		}
		removed, err := txDeleteWaiter(ctx, tx, t.team, key, ref.RunID, ref.NodeID)
		if err != nil {
			return false, err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM concurrency_holders WHERE team=? AND key=? AND run_id=? AND node_id=?`, t.team, key, ref.RunID, ref.NodeID)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if _, err := txPromoteWaiters(ctx, tx, t.team, key, tx.now().UnixNano(), tx.now().Add(DefaultConcurrencyLease).UnixNano()); err != nil {
			return false, err
		}
		return removed || count > 0, nil
	})
}

// ForceReleaseAuthorizedSlots releases superseded holders and promotes waiters under member control authority.
func (t *Tenant) ForceReleaseAuthorizedSlots(ctx context.Context, a capability.Authorized, key string) ([]ConcurrencyHolder, error) {
	return authorizedConcurrency(ctx, t, a, key, "/force-release", func(tx *storeTx, _ capability.Authorized, _ time.Time) ([]ConcurrencyHolder, error) {
		state, err := t.concurrencyStateTx(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		var out []ConcurrencyHolder
		for _, holder := range state.Holders {
			if !holder.Superseded {
				continue
			}
			if _, _, _, err := txReleaseHolder(ctx, tx, t.team, key, holder.HolderID); err != nil {
				return nil, err
			}
			out = append(out, holder)
		}
		_, err = txPromoteWaiters(ctx, tx, t.team, key, tx.now().UnixNano(), tx.now().Add(DefaultConcurrencyLease).UnixNano())
		return out, err
	})
}
