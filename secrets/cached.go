package secrets

import (
	"context"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Source is the read side of a secret store. Implementations: local
// dotenv (DotenvSource.Read) and the controller HTTP client (any
// closure satisfies SourceFunc).
type Source interface {
	Read(name string) (value string, masked bool, err error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(name string) (value string, masked bool, err error)

// Read satisfies Source.
func (f SourceFunc) Read(name string) (string, bool, error) { return f(name) }

// cachedEntry packs a resolved (value, masked) pair so the cache
// hits return both fields without a second source roundtrip. The
// masked flag drives the resolver's strict API check: sparkwing.Secret
// errors on masked=false entries, sparkwing.Config errors on
// masked=true entries, so call sites can't silently drift.
type cachedEntry struct {
	value  string
	masked bool
}

// Cached wraps any Source with a per-instance memoization map and
// optionally a Masker. First successful Read for a given name caches
// the (value, masked) pair and -- when masked=true -- registers the
// value with the masker. Subsequent reads return the cached values
// directly without touching the source.
type Cached struct {
	src    Source
	masker *Masker

	mu    sync.RWMutex
	cache map[string]cachedEntry
}

// NewCached returns a Cached resolver backed by src and recording
// resolved masked values into masker. masker may be nil for tests
// or paths that don't need redaction.
func NewCached(src Source, masker *Masker) *Cached {
	return &Cached{src: src, masker: masker, cache: map[string]cachedEntry{}}
}

// Resolve satisfies sparkwing.SecretResolver. Returns the value and
// the entry's masked flag so the SDK's Secret / Config wrappers can
// enforce strict matching at the call site.
func (c *Cached) Resolve(ctx context.Context, name string) (string, bool, error) {
	c.mu.RLock()
	e, ok := c.cache[name]
	c.mu.RUnlock()
	if ok {
		return e.value, e.masked, nil
	}
	v, masked, err := c.src.Read(name)
	if err != nil {
		return "", false, err
	}
	c.mu.Lock()
	c.cache[name] = cachedEntry{value: v, masked: masked}
	c.mu.Unlock()
	// Only masked entries register with the run's log masker --
	// non-secret config (region, log level) intentionally renders
	// raw in stdout/stderr captures so operators can see what's
	// configured.
	if masked && c.masker != nil {
		c.masker.Register(v)
	}
	return v, masked, nil
}

// AsResolver returns a sparkwing.SecretResolver backed by c. Convenience
// for ctx installation: WithSecretResolver expects the interface, not
// the concrete type.
func (c *Cached) AsResolver() sparkwing.SecretResolver { return c }
