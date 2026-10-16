package store

import (
	"context"
	"time"
)

// ChargeNodeCreditsForTest bills the seconds the node has run since its
// previous charge, as a heartbeat's renewal does.
func ChargeNodeCreditsForTest(ctx context.Context, s *Store, runID, nodeID, tokenPrefix string, now time.Time) (CreditChargeResult, error) {
	return s.chargeNode(ctx, runID, nodeID, tokenPrefix, now, false)
}
