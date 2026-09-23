// Package storagequota holds a free team to its storage allowance in the
// process that writes the bytes. The allowance is split into fixed shares,
// one per store, so no store ever reads another store's count: the cache
// keeps three quarters, the logs service three sixteenths and the
// controller's run events one sixteenth.
package storagequota

// DefaultAllowanceBytes is a free team's allowance when the operator has
// not set storage_free_allowance_bytes: one gibibyte across every store.
const DefaultAllowanceBytes int64 = 1 << 30

// Tier is what a team may store.
type Tier string

const (
	// TierFunded stores without a byte limit: a team with credits, or the
	// operator's own team.
	TierFunded Tier = "funded"
	// TierFree holds a free-tier slot and stores up to its shares.
	TierFree Tier = "free"
	// TierNone has neither credits nor a slot and stores nothing.
	TierNone Tier = "none"
)

// CacheShare is the part of allowance the cache holds a free team to.
func CacheShare(allowance int64) int64 { return allowance / 4 * 3 }

// LogShare is the part of allowance the logs service holds a free team to.
func LogShare(allowance int64) int64 { return allowance / 16 * 3 }

// EventShare is the part of allowance the controller's run events hold a
// free team to.
func EventShare(allowance int64) int64 { return allowance / 16 }
