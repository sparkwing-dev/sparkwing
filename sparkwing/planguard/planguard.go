// Package planguard implements the Plan() purity sentinel.
//
// Pipeline.Plan must be pure-declarative; side effects belong inside
// a Job's Work() body. This package is a sibling of sparkwing/,
// sparkwing/docker, sparkwing/git, and sparkwing/services so every
// layer that ships side-effect helpers can import the same sentinel
// without violating the SDK's layering rule.
//
// The user-facing alias is sparkwing.GuardPlanTime, which delegates
// here.
package planguard

import (
	"context"
	"fmt"
)

type planTimeKey struct{}

// With returns ctx marked as a Plan() invocation context. The caller
// is sparkwing.Registration.Invoke.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, planTimeKey{}, true)
}

// Active reports whether ctx is currently inside a Plan() call.
// Side-effect helpers should prefer Guard; Active is for tests and
// for code that wants to branch quietly on plan-time presence.
func Active(ctx context.Context) bool {
	v, _ := ctx.Value(planTimeKey{}).(bool)
	return v
}

// Guard panics if invoked from inside a Pipeline.Plan() call. `what`
// names the helper that triggered the guard (e.g. "sparkwing.Bash")
// so the panic message tells the author which call to lift into a Job.
func Guard(ctx context.Context, what string) {
	if Active(ctx) {
		panic(fmt.Sprintf(
			"sparkwing: %s called inside Pipeline.Plan() -- Plan() must be pure-declarative; "+
				"move side effects into a Job's Work() body and surface the result via "+
				"sparkwing.Out(...) + Ref[T]. See docs/sdk.md#plan-must-be-pure",
			what,
		))
	}
}
