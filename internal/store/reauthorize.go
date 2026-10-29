package store

import (
	"context"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Reauthorize runs the rule that admitted a long-lived read again, against
// current authority: ancestry, membership, expiry and, for a claim, its run's
// liveness and fence. A stream ends when this refuses.
func (t *Tenant) Reauthorize(ctx context.Context, a capability.Authorized, method, pattern string) error {
	route, ok := capability.LookupRoute(method, pattern)
	if !ok {
		return authorityError(apierr.PolicyMissing, "route policy is unavailable")
	}
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if _, _, err := t.reauthorizeTx(ctx, tx, a, route, false); err != nil {
		return err
	}
	return tx.Commit()
}

// reauthorizeTx authenticates a's authority from the store and authorizes it
// for route under the access rule a was admitted with.
func (t *Tenant) reauthorizeTx(ctx context.Context, tx *storeTx, a capability.Authorized, route capability.RoutePolicy, lock bool) (capability.Authorized, capability.AuthenticationSnapshot, error) {
	if a.Target.Team != string(t.team) {
		return capability.Authorized{}, capability.AuthenticationSnapshot{}, bindingRefusal()
	}
	chain, err := teamAuthorityChainTx(ctx, tx, t.team, a.Principal.Authority, lock, sameTeam)
	if err != nil {
		return capability.Authorized{}, capability.AuthenticationSnapshot{}, err
	}
	snapshot := capability.AuthenticationSnapshot{Now: tx.now(ctx)}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal})
	}
	snapshot.Binding, err = authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return capability.Authorized{}, snapshot, err
	}
	auth, err := capability.AuthenticateSnapshot(snapshot, route, capability.RequestEvidence{Target: a.Target})
	if err != nil {
		return capability.Authorized{}, snapshot, err
	}
	if !reflect.DeepEqual(auth.Principal.Capability.Binding, a.Binding) {
		return capability.Authorized{}, snapshot, bindingRefusal()
	}
	var live []capability.LiveAccess
	for _, option := range auth.Live {
		if reflect.DeepEqual(route.Access[option.RuleIndex], a.Access) {
			live = append(live, option)
		}
	}
	auth.Live = live
	current, err := capability.Authorize(ctx, tx, route, auth, a.Target)
	return current, snapshot, err
}
