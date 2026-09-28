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
