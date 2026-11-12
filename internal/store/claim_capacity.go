package store

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var errClaimCapacityUnavailable = notFound("concurrency capacity", "")

func claimCapacityReadyTx(ctx context.Context, tx *storeTx, prefix string, team Team, unit UnitRef) (bool, error) {
	if unit.Kind != capability.UnitNode {
		return true, nil
	}
	// safety: capacity is a hint until MayStart; its billing reads must not take
	// the ledger before the claim locks.
	probe := *tx
	probe.snapshotRead = true
	tx = &probe
	row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: unit.RunID, NodeID: unit.NodeID})
	if err != nil {
		return false, err
	}
	now := tx.now(ctx)
	if groupWaitExpired(row, now) {
		noteGroupWait(tx, unit, row, now)
		return false, nil
	}
	var host string
	for _, group := range row.Execution.Concurrency {
		if group.OnLimit != "" && group.OnLimit != onLimitQueue || max(group.Cost, 1) > max(group.Capacity, 1) {
			continue
		}
		if group.Scope == "box" && host == "" {
			chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, false)
			if err != nil {
				return false, err
			}
			host = awardExecutionHost(chain, runspec.SealedRequest{}, false)
		}
		key, err := capacityScopeKey(string(unit.RunID), string(unit.NodeID), group.Scope, group.Group, host)
		if errors.Is(err, errLauncherHostPending) {
			continue
		}
		if err != nil {
			return false, err
		}
		acct, err := tx.claimCapacity.read(ctx, tx, team, key, tx.now(ctx).UnixNano())
		if err != nil {
			return false, err
		}
		ready, err := concurrencyFitsTx(ctx, tx, team, acquireSlotRequest{Key: key, HolderID: string(unit.RunID) + "/" + nodeIDOrDash(string(unit.NodeID)), RunID: string(unit.RunID), NodeID: string(unit.NodeID), Policy: onLimitQueue, Capacity: max(group.Capacity, 1), Cost: max(group.Cost, 1)}, tx.now(ctx).UnixNano(), acct, false)
		if err != nil || !ready {
			if err == nil {
				tx.claimCapacity.block(team, key, max(group.Capacity, 1), max(group.Cost, 1), acct)
				noteGroupWait(tx, unit, row, now)
			}
			return false, err
		}
	}
	return true, nil
}
