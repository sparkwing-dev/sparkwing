package controller

import (
	"context"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: ForTeam reads the team registry on every call, and a runner fleet
// polling run routes would pay that read per request. A team removed inside
// the window keeps a live handle for at most this long; removing a team is an
// operator path that drains its rows first.
const tenantCacheTTL = 30 * time.Second

type tenantCache struct {
	mu      sync.Mutex
	entries map[store.Team]tenantCacheEntry
}

type tenantCacheEntry struct {
	tenant  *store.Tenant
	expires time.Time
}

func (c *tenantCache) get(team store.Team, now time.Time) (*store.Tenant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[team]
	if !ok || !now.Before(e.expires) {
		return nil, false
	}
	return e.tenant, true
}

func (c *tenantCache) put(team store.Team, t *store.Tenant, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[store.Team]tenantCacheEntry{}
	}
	c.entries[team] = tenantCacheEntry{tenant: t, expires: now.Add(tenantCacheTTL)}
}

func (s *Server) tenantForTeam(ctx context.Context, team store.Team) (*store.Tenant, error) {
	team = store.NormalizeTeam(team)
	now := time.Now()
	if t, ok := s.tenants.get(team, now); ok {
		return t, nil
	}
	t, err := s.store.ForTeam(ctx, team)
	if err != nil {
		return nil, err
	}
	s.tenants.put(team, t, now)
	return t, nil
}
