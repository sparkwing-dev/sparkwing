package sparkwing

import (
	"log/slog"
	"time"
)

// DefaultCacheTTL is the TTL applied when a node declares [JobNode.Memoize]
// without an explicit [TTL] option.
const DefaultCacheTTL = 7 * 24 * time.Hour

// MaxCacheTTL is the retention ceiling. A higher [TTL] is clamped
// when [JobNode.Memoize] is called and logs a warning.
const MaxCacheTTL = 35 * 24 * time.Hour

// MemoizeConfig holds a node's key function and result retention window.
type MemoizeConfig struct {
	// Key computes the content key after upstream dependencies
	// complete. Return [NoCache] with a nil error to bypass memoization.
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

// Memoize replays a stored result when another node computed the same key.
// Keys identify work across groups and runs. See [CacheKeyFn] for key
// resolution and failure behavior.
//
// Use [JobNode.CacheDir] to restore dependency directories before execution.
// [JobNode.Concurrency] independently bounds how many nodes run at once.
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
// The key must distinguish members whose work differs. A constant key
// makes every member share one result.
func (g *JobGroup) Memoize(key CacheKeyFn, opts ...MemoizeOption) *JobGroup {
	for _, m := range g.Members() {
		m.Memoize(key, opts...)
	}
	return g
}
