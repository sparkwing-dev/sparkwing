package sparkwing

import "context"

// ProfileResolutionContext is the slice of profile state the v0.6
// args-resolver needs at registration-invoke time: the resolved
// default-args map, the active profile name (so predicates like
// [Profile]("prod") fire), and whether that profile is local-only
// (so [Local] / [Remote] gates resolve correctly).
//
// Installed on ctx by the orchestrator via
// internal/sparkwingruntime.WithProfileResolution and read back by
// the sparkwing package's invoke() before it calls
// Schema.Resolve. Pipeline authors don't construct or read this
// type directly.
type ProfileResolutionContext struct {
	// Defaults is the per-arg default map from the active profile's
	// `default-args:` block, with ${VAR} interpolation already
	// applied. Nil when no profile is active or the profile has no
	// default-args.
	Defaults map[string]string

	// Name is the active profile's name (e.g. "prod", "local"), used
	// by the [Profile](name) predicate. Empty when no profile is
	// active.
	Name string

	// IsLocal reports whether the active profile routes through the
	// in-process local SQLite (i.e. the laptop builtin). Drives the
	// [Local] / [Remote] context predicates.
	IsLocal bool
}

type keyProfileResolutionType struct{}

var keyProfileResolution = keyProfileResolutionType{}

// profileResolutionFromContext extracts the framework-installed
// profile-resolution context. Returns the zero value when none is
// installed, which the resolver treats as "no profile defaults, no
// name, not local" -- the same as a vanilla local dispatch with no
// profile chain.
func profileResolutionFromContext(ctx context.Context) ProfileResolutionContext {
	if ctx == nil {
		return ProfileResolutionContext{}
	}
	v := ctx.Value(keyProfileResolution)
	if v == nil {
		return ProfileResolutionContext{}
	}
	if pr, ok := v.(ProfileResolutionContext); ok {
		return pr
	}
	return ProfileResolutionContext{}
}

type keySkipArgResolveType struct{}

var keySkipArgResolve = keySkipArgResolveType{}

// SkipArgResolve marks ctx so the registration's invoke() builds a
// plan without running the v0.6 args resolution+bind pass. Intended
// for describe-time consumers (internal/sparkwingruntime.DescribePipelineByName,
// the risk-label walker, the tab-completion warmer) that need to
// walk the plan graph without erroring on missing required args.
//
// Production dispatch never sets this -- run paths want resolve to
// fire so missing required args fail loud before any step runs.
func SkipArgResolve(ctx context.Context) context.Context {
	return context.WithValue(ctx, keySkipArgResolve, true)
}

// skipArgResolveFromContext reports whether ctx carries the describe-
// mode marker installed by [SkipArgResolve]. Internal to the package.
func skipArgResolveFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(keySkipArgResolve).(bool)
	return v
}
