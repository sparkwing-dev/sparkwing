package sparkwing

import (
	"log/slog"
	"time"
)

// DefaultCacheTTL is the TTL applied when a node declares [JobNode.Memoize]
// without an explicit [TTL] option.
const DefaultCacheTTL = 7 * 24 * time.Hour

// MaxCacheTTL is the ceiling; a [TTL] above this is clamped at call
// time with a warning log. Lets operators pick their own retention
// without enabling unbounded cache growth.
const MaxCacheTTL = 35 * 24 * time.Hour

// MemoizeConfig is a node's resolved memoization configuration: the
// key function that names the work plus the retention window for a
// stored result.
type MemoizeConfig struct {
	// Key computes the content key after upstream dependencies
	// complete. Return [NoCache] to opt this invocation out.
	Key CacheKeyFn
	// TTL bounds how long a stored result remains reusable.
	TTL time.Duration
}

// MemoizeOption tunes a [JobNode.Memoize] declaration.
type MemoizeOption func(*MemoizeConfig)

// TTL sets how long a node's memoized result remains reusable. Values
// above [MaxCacheTTL] are clamped with a warning log; a non-positive
// value falls back to [DefaultCacheTTL].
func TTL(d time.Duration) MemoizeOption {
	return func(c *MemoizeConfig) { c.TTL = d }
}

// Memoize skips re-running the node when its result is already known.
// key names the work; when a later node computes the same key, the
// orchestrator replays the stored result instead of running the node
// at all. Memoization is keyed on content alone -- no scope, no group
// -- so two nodes that happen to share a group never collide on each
// other's results. For bounding how many nodes run at once, use
// [JobNode.Concurrency]; the two are independent.
//
//	shard.Memoize(func(ctx context.Context) sparkwing.CacheKey {
//	    return sparkwing.Key("coverage", "shard-1")
//	}, sparkwing.TTL(7*24*time.Hour))
//
// Memoize is not a dependency cache. A hit means the node does not run;
// it does not restore a directory so the node can run faster. To keep a
// dependency directory warm across runs while the node still runs, use
// [JobNode.CacheDir]. Porting a GitHub Actions `actions/cache` step maps
// to CacheDir, not Memoize.
//
// Repeated calls overwrite. A nil key clears any prior declaration.
func (n *JobNode) Memoize(key CacheKeyFn, opts ...MemoizeOption) *JobNode {
	if key == nil {
		n.contentCache = nil
		return n
	}
	cfg := MemoizeConfig{Key: key}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultCacheTTL
	}
	if cfg.TTL > MaxCacheTTL {
		slog.Warn("sparkwing.Memoize: TTL exceeds MaxCacheTTL; clamping",
			"node", n.id, "requested", cfg.TTL, "max", MaxCacheTTL)
		cfg.TTL = MaxCacheTTL
	}
	n.contentCache = &cfg
	return n
}

// MemoizeConfig returns the node's resolved memoization configuration,
// or nil when [JobNode.Memoize] was not called.
func (n *JobNode) MemoizeConfig() *MemoizeConfig { return n.contentCache }

// Memoize memoizes every member of the group. See [JobNode.Memoize].
//
// It applies one key function to every member, so a matrix built with
// [JobFanOut] needs a key that discriminates per member -- otherwise
// every cell shares one entry and the first cell's result replays for
// the rest (a Go 1.23 cell would serve a Go 1.24 cell's pass). Make the
// key depend on the per-member value (the fanned-out input, or a
// node-specific upstream output); a constant key makes every member
// share one result on purpose.
func (g *JobGroup) Memoize(key CacheKeyFn, opts ...MemoizeOption) *JobGroup {
	for _, m := range g.Members() {
		m.Memoize(key, opts...)
	}
	return g
}
