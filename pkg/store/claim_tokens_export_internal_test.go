package store

import (
	"context"
	"time"
)

// CommitClaimResultForTest commits digest as tok's claim result in a
// transaction of its own, for the external suite to drive the fence both
// result routes share.
func CommitClaimResultForTest(ctx context.Context, s *Store, tok ClaimToken, digest string, now time.Time) (bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	replayed, err := commitClaimResultTx(ctx, tx, tok, digest, now)
	if err != nil {
		return false, err
	}
	return replayed, tx.Commit()
}
