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

// SettleForTest settles runID in a transaction of its own under its run row.
func SettleForTest(ctx context.Context, s *Store, team Team, runID string, now time.Time) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return err
	}
	if err := settleTx(ctx, tx, team, runID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// MaxExpiredClaimRunsPerPass is how many runs one expired-claim pass recovers.
const MaxExpiredClaimRunsPerPass = maxExpiredClaimRunsPerPass
