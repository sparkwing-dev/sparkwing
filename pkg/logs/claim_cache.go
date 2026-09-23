package logs

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// MaxClaimCacheTTL bounds how long the logs service trusts a claim the
// controller confirmed before it asks again.
const MaxClaimCacheTTL = 30 * time.Second

const maxClaimCacheEntries = 4096

var claimHeaders = []string{
	store.ClaimHolderHeader,
	store.ClaimMembershipHeader,
	store.ClaimReservationHeader,
	store.ClaimGenerationHeader,
	store.AttemptOrdinalHeader,
	store.TriggerGenerationHeader,
}

// safety: only an append naming its claim generation or trigger generation
// is cached, because it writes to a substream keyed by that generation; a
// stale holder served from the cache writes only to its own attempt, never
// to the one that replaced it.
func claimCacheKey(r *http.Request, runID, nodeID, credential string) ([sha256.Size]byte, bool) {
	if r.Header.Get(store.ClaimGenerationHeader) == "" && r.Header.Get(store.TriggerGenerationHeader) == "" {
		return [sha256.Size]byte{}, false
	}
	parts := []string{runID, nodeID, credential}
	for _, name := range claimHeaders {
		parts = append(parts, r.Header.Get(name))
	}
	return sha256.Sum256([]byte(strings.Join(parts, "\x00"))), true
}

type claimCache struct {
	mu      sync.Mutex
	expires map[[sha256.Size]byte]time.Time
}

func (c *claimCache) valid(key [sha256.Size]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Before(c.expires[key])
}

func (c *claimCache) remember(key [sha256.Size]byte, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expires == nil || len(c.expires) >= maxClaimCacheEntries {
		now := time.Now()
		kept := map[[sha256.Size]byte]time.Time{}
		for k, t := range c.expires {
			if now.Before(t) && len(kept) < maxClaimCacheEntries/2 {
				kept[k] = t
			}
		}
		c.expires = kept
	}
	c.expires[key] = until
}

// safety: a service that caches no credential caches no claim either.
func (s *Server) claimCacheTTL() time.Duration {
	return min(s.authCacheTTL, MaxClaimCacheTTL)
}
