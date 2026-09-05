package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

func (s *Store) claimSweepWindow(ctx context.Context, key, claimKey string, minInterval, claimTTL time.Duration) (bool, string, error) {
	now := time.Now()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	// safety: the claim row is taken before the stamp is read, under an
	// advisory lock on Postgres, so a claimant that finds the row free has
	// outlived the previous holder's stamp and reads it; reading first let two
	// callers both see "never swept" and both run.
	if err := lockSweepWindowTx(ctx, tx, key); err != nil {
		return false, "", err
	}
	claimCutoff := int64(0)
	if claimTTL > 0 {
		claimCutoff = now.Add(-claimTTL).UnixNano()
	}
	token := strconv.FormatInt(now.UnixNano(), 10)
	res, err := tx.ExecContext(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		 WHERE CAST(sparkwing_meta.value AS BIGINT) <= ?`,
		claimKey, token, now.UnixNano(), claimCutoff)
	if err != nil {
		return false, "", err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if changed == 0 {
		return false, "", nil
	}
	var prev string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM sparkwing_meta WHERE key = ?`, key).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, "", err
	default:
		if minInterval > 0 {
			if lastNS, perr := strconv.ParseInt(prev, 10, 64); perr == nil &&
				now.Sub(time.Unix(0, lastNS)) < minInterval {
				return false, "", nil
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, "", err
	}
	return true, token, nil
}

func (s *Store) stampSweepWindow(ctx context.Context, key string) error {
	now := time.Now()
	_, err := s.exec(ctx,
		`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, strconv.FormatInt(now.UnixNano(), 10), now.UnixNano())
	return err
}

func (s *Store) clearSweepClaim(ctx context.Context, claimKey, token string) error {
	_, err := s.exec(ctx, `DELETE FROM sparkwing_meta WHERE key = ? AND value = ?`, claimKey, token)
	return err
}

func lockSweepWindowTx(ctx context.Context, tx *storeTx, key string) error {
	if tx.dialect != DialectPostgres {
		return nil
	}
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/sweep-window/"+key)
	return err
}
