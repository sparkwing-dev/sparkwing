package nodehost

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func localOwnerRenew(current func(context.Context) (capability.Principal, error), renew func(context.Context, string, bool) (runstate.LeaseBeat, error)) func(context.Context, runstate.Actor, bool) (runstate.LeaseBeat, error) {
	return func(ctx context.Context, actor runstate.Actor, computing bool) (runstate.LeaseBeat, error) {
		principal, err := current(ctx)
		if err != nil {
			return runstate.LeaseBeat{}, err
		}
		c := principal.Capability
		owned := c.Unit == capability.UnitTrigger && c.Binding.Kind == capability.BindingTrigger || c.Unit == capability.UnitPlan && c.Binding.Kind == capability.BindingRun
		fenced := actor.Owner == runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(c.Binding.RunID), Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}
		if fence := c.Binding.Expansion; c.Unit == capability.UnitEval && fence != nil {
			owned, fenced = true, actor.Holder == fence.Holder && actor.Generation == runspec.ClaimGeneration(fence.Generation)
		}
		if !owned || !fenced || c.Prefix == "" || actor.Kind != runstate.ActorRunHost || actor.Authority != principal.Authority || actor.CapabilityPrefix != c.Prefix {
			return runstate.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld, Message: "local renewal differs from its authenticated owner award"}
		}
		return renew(ctx, c.Prefix, computing)
	}
}
