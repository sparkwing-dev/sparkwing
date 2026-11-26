package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"slices"
	"time"
)

// safety: kind names the cap in limit_events, so two limits never count each other's events.
type windowLimit struct {
	kind   string
	limit  int
	window time.Duration
}

var teamInvitationsPerDay = windowLimit{kind: "team_invitations", limit: maxInvitationsPerDay, window: 24 * time.Hour}

// safety: retention keeps events for the longest window listed here, so claimWindowLimitsTx refuses any other limit.
var windowLimits = []windowLimit{teamInvitationsPerDay, invitationEmailsPerAccount, invitationEmailsPerRecipient}

// safety: oldest is zero when the key has no event in the window.
type windowUsage struct {
	count  int
	oldest time.Time
}

func longestLimitWindow() time.Duration {
	var longest time.Duration
	for _, l := range windowLimits {
		longest = max(longest, l.window)
	}
	return longest
}

type limitClaim struct {
	limit windowLimit
	key   string
}

// safety: a refusal names the limit that refused and the usage the caller's error explains.
type windowRefusal struct {
	limit windowLimit
	usage windowUsage
}

// safety: the events are written in the caller's transaction, so a request refused after the claim rolls them back
// and keeps its slots. Keys are locked in sorted order before the clock is read, so concurrent requests never
// deadlock and a lock wait never leaves the window stale; limits are counted in the order given, and the first full
// one refuses the request with nothing written.
func claimWindowLimitsTx(ctx context.Context, tx *storeTx, claims ...limitClaim) (time.Time, *windowRefusal, error) {
	digests := make([]string, len(claims))
	locks := make([]string, len(claims))
	for i, c := range claims {
		if !slices.Contains(windowLimits, c.limit) {
			return time.Time{}, nil, fmt.Errorf("store: window limit %q is not registered", c.limit.kind)
		}
		digests[i] = limitKeyDigest(c.key)
		locks[i] = "limit:" + c.limit.kind + ":" + digests[i]
		// safety: a key named twice would be counted once and recorded twice, overshooting its limit.
		if slices.Contains(locks[:i], locks[i]) {
			return time.Time{}, nil, fmt.Errorf("store: window limit %q names one key twice", c.limit.kind)
		}
	}
	// safety: concurrent claims for one key would each count the other's event as not yet made; the lock orders
	// them. SQLite already serializes every writer.
	for _, lock := range slices.Sorted(slices.Values(locks)) {
		if err := advisoryLockTx(ctx, tx, lock); err != nil {
			return time.Time{}, nil, err
		}
	}
	now := tx.now(ctx)
	if tx.err != nil {
		return time.Time{}, nil, tx.err
	}
	for i, c := range claims {
		var used windowUsage
		var oldest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MIN(at) FROM limit_events WHERE kind = ? AND digest = ? AND at > ?`,
			c.limit.kind, digests[i], now.Add(-c.limit.window).UnixNano()).Scan(&used.count, &oldest); err != nil {
			return time.Time{}, nil, err
		}
		if oldest.Valid {
			used.oldest = time.Unix(0, oldest.Int64).UTC()
		}
		if used.count >= c.limit.limit {
			return now, &windowRefusal{limit: c.limit, usage: used}, nil
		}
	}
	for i, c := range claims {
		if _, err := tx.execContext(ctx, `INSERT INTO limit_events (kind, digest, at) VALUES (?, ?, ?)`,
			c.limit.kind, digests[i], now.UTC().UnixNano()); err != nil {
			return time.Time{}, nil, err
		}
	}
	return now, nil, nil
}

// safety: events outlive the accounts, teams and addresses they count, so the log keeps a digest of the key rather
// than the key. A caller normalizes a key that has several spellings, such as an email address, before claiming.
func limitKeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
