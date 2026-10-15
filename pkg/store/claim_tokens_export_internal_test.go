package store

import (
	"context"
	"time"
)

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

// ChargeNodeCreditsForTest bills the seconds the node has run since its
// previous charge, as a heartbeat's renewal does.
func ChargeNodeCreditsForTest(ctx context.Context, s *Store, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, false)
}
