package capability

import "time"

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
