package sdkserve

import (
	"context"
	"fmt"
)

type planTimeKey struct{}

// With returns ctx marked as a Plan() invocation context. The caller
// is sparkwing.Registration.Invoke.
func DeclarationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, planTimeKey{}, true)
}

// Active reports whether ctx is currently inside a Plan() call.
// Side-effect helpers should prefer Guard; Active is for tests and
// for code that wants to branch quietly on plan-time presence.
func Planning(ctx context.Context) bool {
	v, _ := ctx.Value(planTimeKey{}).(bool)
	return v
}

// Guard panics if invoked from inside a Pipeline.Plan() call. `what`
// names the helper that triggered the guard (e.g. "sparkwing.Bash")
// so the panic message tells the author which call to lift into a Job.
func RequireExecution(ctx context.Context, what string) {
	if Planning(ctx) {
		panic(fmt.Sprintf(
			"sparkwing: %s called inside Pipeline.Plan() -- Plan() must be pure-declarative; "+
				"move side effects into a Job's Work() body and surface the result via "+
				"sparkwing.Step + Ref[T]. See docs/sdk.md#plan-must-be-pure",
			what,
		))
	}
}
