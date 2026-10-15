package capability

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// AuthorityGeneration fences one capability or session in the credential's complete ancestry.
type AuthorityGeneration struct {
	Authority  AuthorityRef
	Generation int64
}

// AuthGenerations includes every ancestor even when a launcher root belongs to another team.
// MembershipGeneration comes from the awarded credential's team, not the ancestor's team.
type AuthGenerations struct {
	Authorities          []AuthorityGeneration
	Team                 string
	MembershipGeneration int64
}

// AuthCacheEntry may be reused only before CachedAt + limits.AuthCacheTTL and with
// fresh, identical AuthGenerations from the store. Missing ancestors or failed reads refuse reuse.
// Route-specific binding liveness and expiry are checked on every request, outside this cache.
type AuthCacheEntry struct {
	Principal   Principal
	Generations AuthGenerations
	CachedAt    time.Time
}

// Reusable compares a fresh store snapshot with the complete cached ancestry.
// It does not replace route liveness checks or authorize a failed generation lookup.
func (entry AuthCacheEntry) Reusable(current AuthGenerations, now time.Time) bool {
	if entry.CachedAt.IsZero() || now.Before(entry.CachedAt) || !now.Before(entry.CachedAt.Add(limits.AuthCacheTTL)) {
		return false
	}
	c := entry.Principal.Capability
	if c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) || c.RevokedAt != nil && !now.Before(*c.RevokedAt) {
		return false
	}
	previous := entry.Generations
	if current.Team != c.Team || previous.Team != current.Team || current.MembershipGeneration < 0 ||
		previous.MembershipGeneration != current.MembershipGeneration || len(previous.Authorities) == 0 || len(previous.Authorities) != len(current.Authorities) {
		return false
	}
	seen := make(map[AuthorityRef]int64, len(previous.Authorities))
	for _, authority := range previous.Authorities {
		if authority.Authority.ID == "" || authority.Generation < 0 || (authority.Authority.Kind != AuthorityCapability && authority.Authority.Kind != AuthoritySession) {
			return false
		}
		if _, duplicate := seen[authority.Authority]; duplicate {
			return false
		}
		seen[authority.Authority] = authority.Generation
	}
	if _, present := seen[entry.Principal.Authority]; !present {
		return false
	}
	if c.Parent != nil {
		if _, present := seen[*c.Parent]; !present {
			return false
		}
	}
	for _, authority := range current.Authorities {
		if generation, present := seen[authority.Authority]; !present || generation != authority.Generation {
			return false
		}
		delete(seen, authority.Authority)
	}
	return len(seen) == 0
}
