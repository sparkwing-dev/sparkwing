package localcontroller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func localOwnerRenew(current func(context.Context) (capability.Principal, error), renew func(context.Context, string, bool) (store.LeaseBeat, error)) func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
	return func(ctx context.Context, actor store.Actor, computing bool) (store.LeaseBeat, error) {
		principal, err := current(ctx)
		if err != nil {
			return store.LeaseBeat{}, err
		}
		c := principal.Capability
		owned := c.Unit == capability.UnitTrigger && c.Binding.Kind == capability.BindingTrigger || c.Unit == capability.UnitPlan && c.Binding.Kind == capability.BindingRun
		fenced := actor.Owner == store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: c.Binding.RunID, Holder: c.Binding.Holder, Generation: c.Binding.Generation}
		if fence := c.Binding.Expansion; c.Unit == capability.UnitEval && fence != nil {
			owned, fenced = true, actor.Holder == fence.Holder && actor.Generation == fence.Generation
		}
		if !owned || !fenced || c.Prefix == "" || actor.Kind != store.ActorRunHost || actor.Authority != principal.Authority || actor.CapabilityPrefix != c.Prefix {
			return store.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld, Message: "local renewal differs from its authenticated owner award"}
		}
		return renew(ctx, c.Prefix, computing)
	}
}
