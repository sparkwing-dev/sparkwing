package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithResolvedArgs installs the v0.6 resolved-args map on ctx so
// step bodies can read individual args via sparkwing.Arg[T] /
// sparkwing.ArgOrDefault. Mirrors the existing WithInputs install
// pattern; called by the orchestrator after the plan's
// resolveAndBindJobArgs pass populates the merged map.
//
// A nil map is a no-op (returns ctx unchanged) so the framework can
// install unconditionally without guarding at call sites.
func WithResolvedArgs(ctx context.Context, args map[string]any) context.Context {
	if args == nil {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.ResolvedArgs, args)
}
