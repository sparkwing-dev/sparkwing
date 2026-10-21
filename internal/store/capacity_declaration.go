package store

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func capacityGroupsTx(ctx context.Context, tx *storeTx, team Team, runID, nodeID string) ([]runspec.Concurrency, error) {
	if nodeID != "" {
		row, err := readNodeTx(ctx, tx, team, controllerwire.NodeRef{RunID: runID, NodeID: nodeID})
		return row.Execution.Concurrency, err
	}
	run, err := readRunTx(ctx, tx, team, runID)
	if err != nil || run.Spec == nil {
		return nil, err
	}
	return run.Spec.Resolved.Settings.Concurrency, nil
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
	if req.Capacity != 0 && req.Capacity != group.Capacity || req.Cost != 0 && req.Cost != group.Cost || req.Policy != "" && req.Policy != group.OnLimit || req.CacheKeyHash != "" || req.CacheTTL != 0 {
		return acquireSlotRequest{}, bindingRefusal()
	}
	req.Capacity, req.Cost, req.Policy = group.Capacity, group.Cost, group.OnLimit
	req.CancelTimeout = 0
	if group.CancelTimeoutMS != nil {
		req.CancelTimeout = time.Duration(*group.CancelTimeoutMS) * time.Millisecond
	}
	return req, nil
}
