package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ReadOwnerSnapshot reads scheduling state and its current owner authority in one snapshot.
// Cancellation remains observable while the owner drains its still-held lease.
func (t *Tenant) ReadOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string) (RunSnapshot, error) {
	return t.readOwnerSnapshot(ctx, authority, runID, func(NodeRef) bool { return true })
}

// ReadOwnerNode reads one node of the owner's run with its private placement decision.
func (t *Tenant) ReadOwnerNode(ctx context.Context, authority capability.AuthorityRef, ref NodeRef) (NodeRow, error) {
	snapshot, err := t.readOwnerSnapshot(ctx, authority, ref.RunID, func(node NodeRef) bool { return node == ref })
	if err != nil {
		return NodeRow{}, err
	}
	for _, node := range snapshot.Nodes {
		if node.Ref == ref {
			return node, nil
		}
	}
	return NodeRow{}, ErrNotFound
}

// ReadOwnerNodes reads the owner's run under the same authority checks without
// placement decisions, which cost one matching read per node.
func (t *Tenant) ReadOwnerNodes(ctx context.Context, authority capability.AuthorityRef, runID string) ([]NodeRow, error) {
	snapshot, err := t.readOwnerSnapshot(ctx, authority, runID, func(NodeRef) bool { return false })
	return snapshot.Nodes, err
}

func (t *Tenant) readOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string, placement func(NodeRef) bool) (RunSnapshot, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer rollbackOrLog(tx)
	p, err := tx.ResolveAuthority(ctx, authority)
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
	if c.Team != string(t.team) || c.Audience != capability.Controller || !slices.Contains(c.Grants, grant) {
		return RunSnapshot{}, bindingRefusal()
	}
	if err := checkRelationTx(ctx, tx, p, rule, capability.Target{RunID: runID}); err != nil {
		return RunSnapshot{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return RunSnapshot{}, err
	}
	if c.Unit != capability.UnitEval && (state.FinishedAt != nil || state.Current.Holder != c.Binding.Holder || state.Current.Generation != c.Binding.Generation || !state.LeaseExpiresAt.After(tx.now())) {
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
			if current, observed, err = ownerExecutionDemandTx(ctx, tx, p, nil); err != nil {
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
		node.OwnerExecution = &OwnerExecution{Owner: authority, Team: t.team, Generation: c.Binding.Generation, Node: node.Ref, NodeGeneration: node.Generation, SpecHash: node.SpecHash, Allowed: allowed}
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
