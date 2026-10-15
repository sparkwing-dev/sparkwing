package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ReadOwnerSnapshot reads scheduling state and its current owner authority in one snapshot.
// Cancellation remains observable while the owner drains its still-held lease.
func (t *Tenant) ReadOwnerSnapshot(ctx context.Context, authority capability.AuthorityRef, runID string) (RunSnapshot, error) {
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
	if c.Team != string(t.team) || c.Audience != capability.Controller || !slices.Contains(c.Grants, capability.TriggerHost) {
		return RunSnapshot{}, bindingRefusal()
	}
	if err := checkRelationTx(ctx, tx, p, capability.BindRunOfTrigger, capability.Target{RunID: runID}); err != nil {
		return RunSnapshot{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, c)
	if err != nil {
		return RunSnapshot{}, err
	}
	if state.FinishedAt != nil || state.Current.Holder != c.Binding.Holder || state.Current.Generation != c.Binding.Generation || !state.LeaseExpiresAt.After(tx.now()) {
		return RunSnapshot{}, bindingRefusal()
	}
	snapshot, err := readSnapshotTx(ctx, tx, t.team, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}
