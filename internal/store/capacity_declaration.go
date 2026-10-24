package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func capacityGroupsTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) ([]runspec.Concurrency, error) {
	if nodeID != "" {
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(runID), NodeID: runspec.NodeID(nodeID)})
		return row.Execution.Concurrency, err
	}
	run, err := readRunTx(ctx, tx, team, runID)
	if err != nil || run.Spec == nil {
		return nil, err
	}
	groups := run.Spec.Resolved.Settings.Concurrency
	released, err := planWorkReleasedTx(ctx, tx, team, runID)
	if err != nil {
		return nil, err
	}
	if released {
		// Released work must finish even if plan continuity has lapsed.
		for i := range groups {
			groups[i].OnLimit, groups[i].QueueTimeoutMS = onLimitQueue, nil
		}
	}
	return groups, nil
}

func planWorkReleasedTx(ctx context.Context, tx *storeTx, team Team, runID string) (bool, error) {
	var released bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE team=? AND run_id=? AND kind IN ('job_ready','job_approval_pending','job_expansion_ready','job_approval_ready'))`, team, runID).Scan(&released)
	return released, err
}

func declaredCapacityGroupTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID, key string, authority capability.AuthorityRef) (runspec.Concurrency, error) {
	level, scope, identity, name, ok := runspec.ParseConcurrencyKey(key)
	if !ok || (nodeID == "") != (level == "plan") || scope == "run" && identity != runID || scope == "box" && identity == "" {
		return runspec.Concurrency{}, bindingRefusal()
	}
	derived, err := capacityKeyTx(ctx, tx, authority, runID, nodeID, scope, name)
	if err != nil {
		return runspec.Concurrency{}, err
	}
	if derived != key {
		return runspec.Concurrency{}, bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, team, runID)
	if err != nil {
		return runspec.Concurrency{}, err
	}
	if run.Spec == nil {
		return runspec.Concurrency{}, bindingRefusal()
	}
	groups, err := capacityGroupsTx(ctx, tx, team, runID, nodeID)
	if err != nil {
		return runspec.Concurrency{}, err
	}
	for _, group := range groups {
		if group.Group == name && group.Scope == scope {
			return group, nil
		}
	}
	return runspec.Concurrency{}, bindingRefusal()
}

func sealedCapacityRequestTx(ctx context.Context, tx *storeTx, team Team, req acquireSlotRequest, authority capability.AuthorityRef) (acquireSlotRequest, error) {
	group, err := declaredCapacityGroupTx(ctx, tx, team, req.RunID, req.NodeID, req.Key, authority)
	if err != nil {
		return acquireSlotRequest{}, err
	}
	if req.Capacity != 0 && req.Capacity != group.Capacity || req.Cost != 0 && req.Cost != group.Cost || req.Policy != "" && req.Policy != group.OnLimit {
		return acquireSlotRequest{}, bindingRefusal()
	}
	req.Capacity, req.Cost, req.Policy = group.Capacity, group.Cost, group.OnLimit
	return req, nil
}
