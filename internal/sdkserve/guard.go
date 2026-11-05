package sdkserve

import (
	"context"
	"fmt"
	"slices"
)

type planTimeKey struct{}

// DeclarationContext returns ctx marked as a Plan() invocation context.
func DeclarationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, planTimeKey{}, true)
}

// Planning reports whether ctx is inside a Plan() call.
// Side-effect helpers should prefer RequireExecution; Planning is for tests and
// for code that wants to branch quietly on plan-time presence.
func Planning(ctx context.Context) bool {
	v, _ := ctx.Value(planTimeKey{}).(bool)
	return v
}

// RequireExecution panics if invoked from inside a Pipeline.Plan() call. `what`
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

type hostFeaturesKey struct{}

// WithHostFeatures records the plan declarations the calling host accepts.
func WithHostFeatures(ctx context.Context, features []string) context.Context {
	return context.WithValue(ctx, hostFeaturesKey{}, features)
}

// HostAccepts reports whether the host that said hello named feature, so an SDK
// leaves out declarations an older host would refuse.
func HostAccepts(ctx context.Context, feature string) bool {
	features, _ := ctx.Value(hostFeaturesKey{}).([]string)
	return slices.Contains(features, feature)
}
