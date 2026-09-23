package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func usageSeconds(t *testing.T, s *store.Store, runID, nodeID string) int64 {
	t.Helper()
	var seconds int64
	if err := s.DB().QueryRow(fmt.Sprintf(
		`SELECT COALESCE(SUM(seconds), 0) FROM credit_charges
		  WHERE run_id = '%s' AND node_id = '%s' AND kind = '%s'`,
		runID, nodeID, store.CreditChargeUsage)).Scan(&seconds); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return seconds
}

// A started node whose runner stopped renewing ran until its lease lapsed,
// so the seconds between its last charge and the lease's end are billed
// before the claim is cleared, bounded by the per-charge cap.
func TestExpiredStartedClaimsSettleTheirLastInterval(t *testing.T) {
	for _, gap := range []struct {
		name                 string
		chargedAgo, leaseAgo time.Duration
		wantLow, wantHigh    int64
	}{
		{"short gap", 40 * time.Second, 28 * time.Second, 11, 13},
		{"gap above the charge cap", 200 * time.Second, 5 * time.Second, store.DefaultCreditMaxChargeSeconds, store.DefaultCreditMaxChargeSeconds},
	} {
		for _, tc := range []struct {
			name    string
			recover func(*store.Store, context.Context) error
		}{
			{"reap", func(s *store.Store, ctx context.Context) error {
				_, err := s.ReapExpiredNodeClaims(ctx)
				return err
			}},
			{"agent loss", func(s *store.Store, ctx context.Context) error {
				_, err := store.Maintenance.RecoverExpiredNodeClaims(s, ctx)
				return err
			}},
		} {
			t.Run(gap.name+"/"+tc.name, func(t *testing.T) {
				s := storetest.Open(t)
				ctx := context.Background()
				runID := "run-lapsed"
				claimant, _ := fundedMeteredNode(t, s, runID)
				n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
				if err != nil || n == nil {
					t.Fatalf("claim: %v", err)
				}
				now := time.Now()
				rewindChargeWindow(t, s, n.RunID, n.NodeID, now.Add(-gap.chargedAgo))
				if _, err := s.DB().Exec(fmt.Sprintf(
					`UPDATE nodes SET credit_billing_from = %d WHERE run_id = '%s' AND node_id = '%s'`,
					now.Add(-gap.chargedAgo-20*time.Second).UnixNano(), n.RunID, n.NodeID)); err != nil {
					t.Fatal(err)
				}
				expireNodeLease(t, s, n.RunID, n.NodeID, now.Add(-gap.leaseAgo))
				if err := tc.recover(s, ctx); err != nil {
					t.Fatalf("recover: %v", err)
				}
				if got := usageSeconds(t, s, n.RunID, n.NodeID); got < gap.wantLow || got > gap.wantHigh {
					t.Fatalf("usage billed = %d seconds, want %d to %d: the seconds to the lease's end, capped per charge",
						got, gap.wantLow, gap.wantHigh)
				}
			})
		}
	}
}
