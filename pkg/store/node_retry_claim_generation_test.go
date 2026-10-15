package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A lease-expiry requeue is the same attempt continuing, so its claim keeps the
// generation that says the wait already elapsed.
func TestReapExpiredNodeClaims_KeepsTheClaimGeneration(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_requeue"}

	readyNode(t, s, "run-requeue", "node-a")
	if _, err := s.ClaimNextReadyNode(ctx, identity, "holder-1", time.Minute, nil); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), "run-requeue", "node-a"); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, err := s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatalf("ReapExpiredNodeClaims: %v", err)
	}

	second, err := s.ClaimNextReadyNode(ctx, identity, "holder-2", time.Minute, nil)
	if err != nil {
		t.Fatalf("requeue claim: %v", err)
	}
	if second.ClaimGeneration < 2 {
		t.Errorf("requeue claim generation = %d, want the attempt to carry on past 1", second.ClaimGeneration)
	}
}
