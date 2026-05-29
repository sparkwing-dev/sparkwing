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
// Pass a zero-valued struct (or nil-equivalent fields) to skip the
// install. A truly zero context is treated by the resolver as "no
// profile defaults, no name, local execution".
func WithProfileResolution(ctx context.Context, pr sparkwing.ProfileResolutionContext) context.Context {
	if pr.Defaults == nil && pr.Name == "" && !pr.IsLocal {
		return ctx
	}
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.ProfileResolution, pr)
}
