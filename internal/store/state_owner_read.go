package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// ReadOwnerSnapshot reads scheduling state and its current owner authority in one snapshot.
// Cancellation remains observable while the owner drains its still-held lease.
func (t *Tenant) ReadOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string) (RunSnapshot, error) {
	return t.readOwnerSnapshot(ctx, authority, runID, nil)
}

// ReadOwnerNode reads one node under the live owner's authority without projecting placement.
func (t *Tenant) ReadOwnerNode(ctx context.Context, authority capability.AuthorityRef, ref runspec.NodeRef) (runstate.NodeRow, error) {
	snapshot, err := t.readOwnerSnapshot(ctx, authority, string(ref.RunID), &ref)
	if err != nil {
		return runstate.NodeRow{}, err
	}
	for _, node := range snapshot.Nodes {
		if node.Ref == ref {
			return node, nil
		}
	}
	return runstate.NodeRow{}, ErrNotFound
}

func (t *Tenant) readOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string, target *runspec.NodeRef) (RunSnapshot, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	p, err := resolveTeamAuthorityTx(ctx, tx, t.team, authority, sameTeam)
	if err != nil {
		return RunSnapshot{}, err
	}
	c := p.Capability
	rule, grant := capability.BindRunOfTrigger, capability.TriggerHost
	switch c.Unit {
	case capability.UnitPlan:
		rule, grant = capability.BindPlanOwner, capability.DataRead
	case capability.UnitEval:
		rule, grant = capability.BindSourceReport, capability.DataRead
	}
	if c.Audience != capability.Controller || !slices.Contains(c.Grants, grant) {
		return RunSnapshot{}, bindingRefusal()
	}
	if err := checkRelationTx(ctx, tx, p, rule, capability.Target{RunID: runID}); err != nil {
		return RunSnapshot{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return RunSnapshot{}, err
	}
	if c.Unit != capability.UnitEval && (state.FinishedAt != nil || state.Current.Holder != c.Binding.Holder || state.Current.Generation != c.Binding.Generation || !state.LeaseExpiresAt.After(tx.now(ctx))) {
		return RunSnapshot{}, bindingRefusal()
	}
	var snapshot RunSnapshot
	if target == nil {
		snapshot, err = readSnapshotTx(ctx, tx, t.team, runID)
	} else {
		snapshot.StoreNow = tx.now(ctx)
		snapshot.Run, err = readRunTx(ctx, tx, t.team, runID)
		if err == nil {
			var node runstate.NodeRow
			node, err = readNodeTx(ctx, tx, t.team, *target)
			snapshot.Nodes = []runstate.NodeRow{node}
		}
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	var eligible map[runspec.NodeRef]bool
	loaded := false
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if c.Unit != capability.UnitTrigger || target != nil {
			continue
		}
		if !loaded {
			demand, observed, err := ownerExecutionDemandTx(ctx, tx, p)
			if err != nil {
				return RunSnapshot{}, err
			}
			if observed {
				eligible, err = ownerEligibleNodesTx(ctx, tx, p, demand)
				if err != nil {
					return RunSnapshot{}, err
				}
			}
			loaded = true
		}
		allowed := eligible[node.Ref]
		node.OwnerExecution = &runstate.OwnerExecution{Owner: authority, Team: string(t.team), Generation: c.Binding.Generation, Node: node.Ref, NodeGeneration: int64(node.Generation), SpecHash: node.SpecHash, Allowed: allowed}
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
