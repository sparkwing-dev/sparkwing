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
	return t.readOwnerSnapshot(ctx, authority, runID, func(runspec.NodeRef) bool { return true })
}

// ReadOwnerNode reads one node of the owner's run with its private placement decision.
func (t *Tenant) ReadOwnerNode(ctx context.Context, authority capability.AuthorityRef, ref runspec.NodeRef) (runstate.NodeRow, error) {
	snapshot, err := t.readOwnerSnapshot(ctx, authority, string(ref.RunID), func(node runspec.NodeRef) bool { return node == ref })
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

// ReadOwnerNodes reads the owner's run under the same authority checks without
// placement decisions, which cost one matching read per node.
func (t *Tenant) ReadOwnerNodes(ctx context.Context, authority capability.AuthorityRef, runID string) ([]runstate.NodeRow, error) {
	snapshot, err := t.readOwnerSnapshot(ctx, authority, runID, func(runspec.NodeRef) bool { return false })
	return snapshot.Nodes, err
}

func (t *Tenant) readOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string, placement func(runspec.NodeRef) bool) (RunSnapshot, error) {
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
	snapshot, err := readSnapshotTx(ctx, tx, t.team, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	var demand *UnitDemand
	observed := true
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if c.Unit != capability.UnitTrigger || !placement(node.Ref) {
			continue
		}
		if demand == nil {
			var current UnitDemand
			if current, observed, err = ownerExecutionDemandTx(ctx, tx, p); err != nil {
				return RunSnapshot{}, err
			}
			demand = &current
		}
		allowed := false
		if observed {
			if allowed, err = ownerDemandEligibleTx(ctx, tx, p, *demand, node.Ref); err != nil {
				return RunSnapshot{}, err
			}
		}
		node.OwnerExecution = &runstate.OwnerExecution{Owner: authority, Team: string(t.team), Generation: c.Binding.Generation, Node: node.Ref, NodeGeneration: int64(node.Generation), SpecHash: node.SpecHash, Allowed: allowed}
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
