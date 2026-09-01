package secrets

import (
	"context"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Source interface {
	Read(name string) (value string, masked bool, err error)
}

type SourceFunc func(name string) (value string, masked bool, err error)

func (f SourceFunc) Read(name string) (string, bool, error) { return f(name) }

type cachedEntry struct {
	value  string
	masked bool
}

type Cached struct {
	src    Source
	masker *Masker

	mu    sync.RWMutex
	cache map[string]cachedEntry
}

func NewCached(src Source, masker *Masker) *Cached {
	return &Cached{src: src, masker: masker, cache: map[string]cachedEntry{}}
}

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
	if masked && c.masker != nil {
		c.masker.Register(v)
	}
	return v, masked, nil
}

func (c *Cached) AsResolver() sparkwing.SecretResolver { return c }
