package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// claimSweepWindow reports whether at least minInterval has elapsed since
// the timestamp stored at key in sparkwing_meta, and atomically stamps the
// current time when it has. A racing process that opens the same window
// within minInterval reads the fresh stamp and gets false, so at most one
// caller per interval proceeds. A minInterval of zero or less always
// claims.
//
// The transaction takes the store's write lock at BEGIN, so concurrent
// daemonless processes serialize through the read-compare-stamp and only
// one wins the window.
func (s *Store) claimSweepWindow(ctx context.Context, key string, minInterval time.Duration) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	var prev string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`+s.forUpdate(), key).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Never swept; fall through and claim the first window.
	case err != nil:
		return false, err
	default:
		if minInterval > 0 {
			if lastNS, perr := strconv.ParseInt(prev, 10, 64); perr == nil &&
				now.Sub(time.Unix(0, lastNS)) < minInterval {
				return false, tx.Commit()
			}
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, strconv.FormatInt(now.UnixNano(), 10), now.UnixNano()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
