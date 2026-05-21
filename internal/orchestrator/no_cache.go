package orchestrator

import "context"

// noCacheRunsKey scopes the --no-cache context value installed by
// withNoCacheRuns. Bypass is read-only at the SDK boundary; cache
// writes still happen on success so subsequent runs over the same
// content hit cache normally.
type noCacheRunsKey struct{}

// withNoCacheRuns marks ctx so runNodeWithCache forwards
// BypassRead=true into AcquireSlotRequest. Per-node CacheKeyFn still
// runs (so the write at release-time records a real key); only the
// up-front lookup is suppressed.
func withNoCacheRuns(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheRunsKey{}, true)
}

// noCacheRunsFromContext returns whether the current run was started
// with --no-cache.
func noCacheRunsFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(noCacheRunsKey{}).(bool)
	return v
}
