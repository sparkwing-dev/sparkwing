package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var errLauncherHostPending = errors.New("launcher execution host awaits activation")

// AdmitCapacity derives all capacity inputs from the accepted unit and its live owner award.
// Repeated calls retain holders, resolve waiters and acquire remaining groups in key order.
func (t *Tenant) AdmitCapacity(ctx context.Context, binding CapacityBinding, actor Actor) (AcquireSlotResponse, error) {
	var result AcquireSlotResponse
	err := t.s.inTx(ctx, func(tx *storeTx) (err error) {
		fence := capacityFence{binding, actor}
		run, err := readRunTx(ctx, tx, t.team, binding.RunID)
		if err != nil {
			return err
		}
		if run.Spec == nil {
			return bindingRefusal()
		}
		groups, err := capacityGroupsTx(ctx, tx, t.team, binding.RunID, binding.NodeID)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(groups))
		for _, group := range groups {
			key, err := capacityKeyTx(ctx, tx, actor.Authority, binding.RunID, group.Scope, group.Group)
			if errors.Is(err, errLauncherHostPending) {
				if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, nil, fence); err != nil {
					return err
				}
				if err := checkCapacityUnitTx(ctx, tx, t.team, fence); err != nil {
					return err
				}
				result = AcquireSlotResponse{Kind: AcquireQueued}
				return nil
			}
			if err != nil {
				return err
			}
			keys = append(keys, key)
		}
		slices.SortFunc(keys, strings.Compare)
		if err := prepareCapacityAcquisitionTx(ctx, tx, t.team, keys, fence); err != nil {
			return err
		}
		id := binding.RunID + "/" + binding.NodeID
		if binding.NodeID == "" {
			id += "-"
		}
		result = AcquireSlotResponse{Kind: AcquireGranted, HolderID: id}
		var touched []string
		defer func() {
			if err != nil {
				return
			}
			if err = checkCapacityOwnerTx(ctx, tx, t.team, fence); err != nil {
				return
			}
			for _, key := range touched {
				if err = txCheckConcurrencyInvariants(ctx, tx, t.team, key, tx.now().UnixNano()); err != nil {
					return
				}
			}
		}()
		for _, key := range keys {
			touched = append(touched, key)
			if err := txLockEntry(ctx, tx, t.s.forUpdate(), t.team, key); err != nil {
				return err
			}
			adopted, err := adoptCapacityTx(ctx, tx, t.team, key, id, binding)
			if err != nil {
				return err
			}
			switch adopted {
			case adoptHolder:
				continue
			case adoptWaiter:
				resolved, err := t.resolveWaiterTx(ctx, tx, key, binding.RunID, binding.NodeID, "", "", defaultConcurrencyLease, &fence)
				if err != nil {
					return err
				}
				switch resolved.Status {
				case waiterPromoted:
					continue
				case WaiterStillWaiting:
					result = AcquireSlotResponse{Kind: AcquireQueued, Position: resolved.Position, QueueLength: resolved.QueueLength, Holders: resolved.Holders}
				case waiterExpired:
					result = AcquireSlotResponse{Kind: AcquireFailed, FailureReason: FailureQueueTimeout}
				case waiterCancelled:
					return ErrConcurrencySuperseded
				default:
					return bindingRefusal()
				}
			case acquireAgain:
				result, err = t.acquireConcurrencySlotTx(ctx, tx, AcquireSlotRequest{Key: key, HolderID: id, RunID: binding.RunID, NodeID: binding.NodeID}, &fence, time.Time{})
				if err != nil {
					return err
				}
				if result.Kind == AcquireGranted || result.Kind == AcquireCancellingOthers {
					continue
				}
			default:
				return bindingRefusal()
			}
			return nil
		}
		var expires sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MIN(lease_expires_at) FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=? AND holder_id=? AND superseded=0`, t.team, binding.RunID, binding.NodeID, id).Scan(&expires); err != nil {
			return err
		}
		result.Kind, result.HolderID = AcquireGranted, id
		if expires.Valid {
			result.LeaseExpiresAt = time.Unix(0, expires.Int64)
		}
		return nil
	})
	return result, err
}

// capacityKeyTx reads only the execution identity sealed on the authenticated award.
func capacityKeyTx(ctx context.Context, tx *storeTx, authority capability.AuthorityRef, runID, scope, group string) (string, error) {
	switch scope {
	case "global":
		return runspec.ConcurrencyKey(scope, "", group), nil
	case "run":
		return runspec.ConcurrencyKey(scope, runID, group), nil
	case "box":
		chain, err := authorityChainTx(ctx, tx, authority, false)
		if err != nil {
			return "", err
		}
		c := chain[0].principal.Capability
		if c.Unit != "" && c.ExecutionHost != "" {
			return runspec.ConcurrencyKey(scope, c.ExecutionHost, group), nil
		}
		if c.Unit != "" {
			for _, node := range chain {
				if capability.Launcher(node.principal.Capability) {
					return "", errLauncherHostPending
				}
			}
		}
	}
	return "", bindingRefusal()
}
