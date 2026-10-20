package controller

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// A team removed inside the window keeps its handle only until the entry
// expires.
func TestTenantCacheEntriesExpire(t *testing.T) {
	var c tenantCache
	now := time.Now()
	c.put("acme", &store.Tenant{}, now)
	if _, ok := c.get("acme", now.Add(tenantCacheTTL-time.Nanosecond)); !ok {
		t.Fatal("a fresh entry missed")
	}
	if _, ok := c.get("acme", now.Add(tenantCacheTTL)); ok {
		t.Fatal("an entry outlived its TTL")
	}
}
