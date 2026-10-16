package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func billingFrom(t *testing.T, s *store.Store, runID, nodeID string) int64 {
	t.Helper()
	var from int64
	if err := s.DB().QueryRow(fmt.Sprintf(
		`SELECT credit_billing_from FROM nodes WHERE run_id = '%s' AND node_id = '%s'`,
		runID, nodeID)).Scan(&from); err != nil {
		t.Fatalf("read the billing start: %v", err)
	}
	return from
}

func fundedMeteredNode(t *testing.T, s *store.Store, runID string) (store.ClaimIdentity, int64) {
	t.Helper()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, runID, "build")
	granted := int64(100 * store.MicroCreditsPerCent)
	if _, err := tenant(t, s, store.DefaultTeam).TestOnlySettlePaidCredit(context.Background(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: granted, Reference: "pay_" + runID, CreatedBy: "admin"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	return claimant, granted
}

func dispatchedClaim(t *testing.T, s *store.Store, claimant store.ClaimIdentity, runID string) *store.Node {
	t.Helper()
	_, err := s.ClaimUnit(context.Background(), store.UnitDemand{CapabilityPrefix: claimant.TokenPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runID, NodeID: "build"}, Available: match.Resources{Cores: 128}})
	if err != nil {
		t.Fatalf("dispatched claim: %v", err)
	}
	n, err := teststore.ForTeam(t, s, store.DefaultTeam).GetNode(context.Background(), runID, "build")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustBalance(t *testing.T, s *store.Store) int64 {
	t.Helper()
	b, err := tenant(t, s, store.DefaultTeam).CreditBalanceMicro(context.Background())
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}
