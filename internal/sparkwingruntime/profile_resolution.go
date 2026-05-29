package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithProfileResolution installs the v0.6 profile-resolution context
// (default-args map, profile name, local/remote flag) on ctx so the
// SDK's Schema.Resolve sees them at registration-invoke time.
// Counterpart to WithResolvedArgs on the post-resolve side.
//
// Pass a zero-valued struct to skip the install. A truly zero
// context is treated by the resolver as "no profile name, not local"
// -- predicates like Local() and Profile(name) evaluate to false.
func WithProfileResolution(ctx context.Context, pr sparkwing.ProfileResolutionContext) context.Context {
	if pr.Name == "" && !pr.IsLocal {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.ProfileResolution, pr)
}
