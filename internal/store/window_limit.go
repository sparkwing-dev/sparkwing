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

// safety: retention keeps events for the longest window listed here, so claimWindowLimitTx refuses any other limit.
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

// safety: the event is written in the caller's transaction, so a request refused after the claim rolls it back and
// keeps its slot; at the limit the claim writes nothing and reports the usage the refusal names.
func claimWindowLimitTx(ctx context.Context, tx *storeTx, l windowLimit, key string, now time.Time) (windowUsage, bool, error) {
	if !slices.Contains(windowLimits, l) {
		return windowUsage{}, false, fmt.Errorf("store: window limit %q is not registered", l.kind)
	}
	digest := limitKeyDigest(key)
	// safety: concurrent claims for one key would each count the other's event as not yet made; the lock orders
	// them. SQLite already serializes every writer.
	if err := advisoryLockTx(ctx, tx, "limit:"+l.kind+":"+digest); err != nil {
		return windowUsage{}, false, err
	}
	var used windowUsage
	var oldest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MIN(at) FROM limit_events WHERE kind = ? AND digest = ? AND at > ?`,
		l.kind, digest, now.Add(-l.window).UnixNano()).Scan(&used.count, &oldest); err != nil {
		return windowUsage{}, false, err
	}
	if oldest.Valid {
		used.oldest = time.Unix(0, oldest.Int64).UTC()
	}
	if used.count >= l.limit {
		return used, false, nil
	}
	if _, err := tx.execContext(ctx, `INSERT INTO limit_events (kind, digest, at) VALUES (?, ?, ?)`,
		l.kind, digest, now.UTC().UnixNano()); err != nil {
		return windowUsage{}, false, err
	}
	return used, true, nil
}

// safety: events outlive the accounts, teams and addresses they count, so the log keeps a digest of the key rather
// than the key. A caller normalizes a key that has several spellings, such as an email address, before claiming.
func limitKeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
