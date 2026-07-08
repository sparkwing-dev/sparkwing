package sparkwing

import (
	"log/slog"
	"time"
)

// DefaultCacheTTL is the TTL applied when a node declares [JobNode.Cache]
// without an explicit [TTL] option.
const DefaultCacheTTL = 7 * 24 * time.Hour

// MaxCacheTTL is the ceiling; a [TTL] above this is clamped at call
// time with a warning log. Lets operators pick their own retention
// without enabling unbounded cache growth.
const MaxCacheTTL = 35 * 24 * time.Hour

// CacheConfig is a node's resolved content-cache configuration: the
// key function that names the work plus the retention window for a
// stored result.
type CacheConfig struct {
	// Key computes the content key after upstream dependencies
	// complete. Return [NoCache] to opt this invocation out.
	Key CacheKeyFn
	// TTL bounds how long a stored result remains reusable.
	TTL time.Duration
}

// CacheOption tunes a [JobNode.Cache] declaration.
type CacheOption func(*CacheConfig)

// TTL sets how long a node's memoized result remains reusable. Values
// above [MaxCacheTTL] are clamped with a warning log; a non-positive
// value falls back to [DefaultCacheTTL].
func TTL(d time.Duration) CacheOption {
	return func(c *CacheConfig) { c.TTL = d }
}

// Cache memoizes the node's result on content. key names the work;
// when a later node computes the same key, the orchestrator replays
// the stored result instead of re-running. Caching is keyed on content
// alone -- no scope, no group -- so two nodes that happen to share a
// group never collide on each other's results. For bounding how many
// nodes run at once, use [JobNode.Concurrency]; the two are
// independent.
//
//	shard.Cache(func(ctx context.Context) sparkwing.CacheKey {
//	    return sparkwing.Key("coverage", "shard-1")
//	}, sparkwing.TTL(7*24*time.Hour))
//
// Repeated calls overwrite. A nil key clears any prior declaration.
func (n *JobNode) Cache(key CacheKeyFn, opts ...CacheOption) *JobNode {
	if key == nil {
		n.contentCache = nil
		return n
	}
	cfg := CacheConfig{Key: key}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultCacheTTL
	}
	if cfg.TTL > MaxCacheTTL {
		slog.Warn("sparkwing.Cache: TTL exceeds MaxCacheTTL; clamping",
			"node", n.id, "requested", cfg.TTL, "max", MaxCacheTTL)
		cfg.TTL = MaxCacheTTL
	}
	n.contentCache = &cfg
	return n
}

// CacheConfig returns the node's resolved content-cache configuration,
// or nil when [JobNode.Cache] was not called.
func (n *JobNode) CacheConfig() *CacheConfig { return n.contentCache }

// Cache memoizes every member of the group on content. See
// [JobNode.Cache]. Members typically want distinct keys, so this is
// most useful when the key function discriminates per node (e.g. via
// node-specific upstream output); a constant key makes every member
// share one result.
func (g *JobGroup) Cache(key CacheKeyFn, opts ...CacheOption) *JobGroup {
	for _, m := range g.Members() {
		m.Cache(key, opts...)
	}
	return g
}
