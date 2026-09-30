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

// MaxClaimTokenCacheTTL bounds how long a claim token's confirmation is
// trusted, so a revoked or cancelled claim stops writing within it while the
// controller is asked once per claim per interval rather than once per write.
const MaxClaimTokenCacheTTL = 5 * time.Second

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
// to the one that replaced it. A claim token is its own attempt, so the whole
// token keys its entry.
func claimCacheKey(r *http.Request, runID, nodeID, credential string, claimToken bool) ([sha256.Size]byte, bool) {
	if !claimToken && r.Header.Get(store.ClaimGenerationHeader) == "" && r.Header.Get(store.TriggerGenerationHeader) == "" {
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
	entries map[[sha256.Size]byte]claimEntry
}

// safety: a claim token's entry keeps the team the controller bound it to and
// the attempt it names, so a write served from the cache is still labeled and
// still lands in that attempt's own stream.
type claimEntry struct {
	until   time.Time
	team    string
	attempt appendIdentity
}

func (c *claimCache) valid(key [sha256.Size]byte, now time.Time) (claimEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	return e, now.Before(e.until)
}

func (c *claimCache) remember(key [sha256.Size]byte, e claimEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxClaimCacheEntries {
		now := time.Now()
		kept := map[[sha256.Size]byte]claimEntry{}
		for k, old := range c.entries {
			if now.Before(old.until) && len(kept) < maxClaimCacheEntries/2 {
				kept[k] = old
			}
		}
		c.entries = kept
	}
	c.entries[key] = e
}

// safety: a service that caches no credential caches no claim either.
func (s *Server) claimCacheTTL() time.Duration {
	return min(s.authCacheTTL, MaxClaimCacheTTL)
}
