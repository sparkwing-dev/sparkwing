package orchestrator

import "context"

// noCacheKey scopes the --no-cache context value installed by
// withNoCache. Bypass is read-only at the SDK boundary; cache
// writes still happen on success so subsequent runs over the same
// content hit cache normally.
type noCacheKey struct{}

// withNoCache marks ctx so runNodeWithCache forwards
// BypassRead=true into AcquireSlotRequest. Per-node CacheKeyFn still
// runs (so the write at release-time records a real key); only the
// up-front lookup is suppressed.
func withNoCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey{}, true)
}

// noCacheFromContext returns whether the current run was started
// with --no-cache.
func noCacheFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(noCacheKey{}).(bool)
	return v
}
