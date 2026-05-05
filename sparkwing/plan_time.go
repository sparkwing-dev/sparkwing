package sparkwing

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// Enforces Plan() purity. Pipeline.Plan(ctx, in, rc) is
// pure-declarative; side effects belong inside a Job's Work() body.
// Violations panic at runtime via the sentinel below.
//
// The actual sentinel lives in sparkwing/planguard so SDK
// sub-packages (docker, git, services) can import it without
// violating the layering rule. The names here are user-facing
// aliases.

// withPlanTime marks ctx as the Plan() invocation context.
func withPlanTime(ctx context.Context) context.Context {
	return planguard.With(ctx)
}

// GuardPlanTime panics if invoked from inside a Pipeline.Plan() call.
// `what` names the helper that triggered the guard (e.g.
// "sparkwing.Bash") so the panic message tells the author exactly
// which call to lift into a Job. Custom helpers can guard their own
// ctx-taking entry points by calling this.
func GuardPlanTime(ctx context.Context, what string) {
	planguard.Guard(ctx, what)
}

// IsPlanTime reports whether ctx is currently inside a Plan() call.
// Mostly for tests; production code should prefer GuardPlanTime.
func IsPlanTime(ctx context.Context) bool {
	return planguard.Active(ctx)
}
