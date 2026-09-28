package store

import (
	"context"
	"time"
)

// CommitClaimResultForTest commits c in a transaction of its own, standing in
// for the store function a result route writes through.
func CommitClaimResultForTest(ctx context.Context, s *Store, c ClaimResultCommit, now time.Time) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	replayed, err := c.commitTx(ctx, tx, now)
	if err != nil {
		return false, err
	}
	return replayed, tx.Commit()
}

// AssertClaimSensitiveForTest runs the sensitive-write fence in a transaction
// of its own.
func AssertClaimSensitiveForTest(ctx context.Context, s *Store, tok ClaimToken, now time.Time) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	return assertClaimSensitiveTx(ctx, tx, tok, now)
}

// HoldClaimSensitiveForTest runs the sensitive-write fence and leaves its
// transaction open, standing in for a sensitive write still in flight. It
// returns the Postgres backend holding the transaction and the rollback that
// ends it.
func HoldClaimSensitiveForTest(ctx context.Context, s *Store, tok ClaimToken, now time.Time) (int, func() error, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, nil, err
	}
	if err := assertClaimSensitiveTx(ctx, tx, tok, now); err != nil {
		_ = tx.Rollback()
		return 0, nil, err
	}
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		_ = tx.Rollback()
		return 0, nil, err
	}
	return pid, tx.Rollback, nil
}
