package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
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
	if _, err := tenant(t, s, store.DefaultTeam).GrantCredits(context.Background(), store.CreditGrantPaid, granted, "pay_"+runID, "admin"); err != nil {
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
	n, err := s.GetNode(context.Background(), runID, "build")
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

func TestDispatchedClaimBillsFromItsExecutionStartWhenThePodNeverRenewed(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, granted := fundedMeteredNode(t, s, "run-dispatched")
	n := dispatchedClaim(t, s, claimant, "run-dispatched")
	if from := billingFrom(t, s, n.RunID, n.NodeID); from != 0 {
		t.Fatalf("a dispatched claim started billing at %d before its pod existed", from)
	}

	wrong := store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration + 1,
		AttemptOrdinal: n.AttemptsConsumed + 1,
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, wrong); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong execution attempt = %v, want ErrLockHeld", err)
	}
	if from := billingFrom(t, s, n.RunID, n.NodeID); from != 0 {
		t.Fatalf("a refused execution start began billing at %d", from)
	}

	startedAt := acknowledgeClaimedExecution(t, s, claimant, n)
	anchor := chargeWindowAnchor(t, s, n.RunID, n.NodeID)
	if want := startedAt.Add(store.MinBillableSeconds * time.Second).UnixNano(); anchor != want {
		t.Fatalf("charge window = %d, want the minimum from the execution start, %d", anchor, want)
	}
	if from := billingFrom(t, s, n.RunID, n.NodeID); from != startedAt.UnixNano() {
		t.Fatalf("billing start = %d, want the execution start %d", from, startedAt.UnixNano())
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		AttemptOrdinal: n.AttemptsConsumed + 1,
	}); err != nil {
		t.Fatalf("duplicate execution start: %v", err)
	}
	if again := chargeWindowAnchor(t, s, n.RunID, n.NodeID); again != anchor {
		t.Fatalf("a duplicate execution start moved the window from %d to %d", anchor, again)
	}

	res, err := s.FinalizeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, startedAt.Add(4*time.Second))
	if err != nil || res.Charge != nil {
		t.Fatalf("finalize = %+v, %v; want no row: four seconds pay the minimum", res.Charge, err)
	}
	if got, want := mustBalance(t, s), granted-store.MinBillableSeconds*unpinnedNodeRateMicro; got != want {
		t.Fatalf("balance = %d, want %d", got, want)
	}
}

func TestDispatchedClaimBillsFromThePodsFirstRenewal(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, granted := fundedMeteredNode(t, s, "run-renewed")
	n := dispatchedClaim(t, s, claimant, "run-renewed")

	// safety: forty seconds of provisioning pass before the pod renews, and
	// none of them may be billed.
	renewed := time.Now().Add(40 * time.Second)
	res, err := store.ChargeNodeCreditsForTest(ctx, s, n.RunID, n.NodeID, claimant.TokenPrefix, renewed)
	if err != nil {
		t.Fatalf("first renewal: %v", err)
	}
	if res.Charge != nil {
		t.Fatalf("the first renewal wrote %+v; it opens the window and bills nothing", res.Charge)
	}
	if from := billingFrom(t, s, n.RunID, n.NodeID); from != renewed.UnixNano() {
		t.Fatalf("billing start = %d, want the first renewal %d", from, renewed.UnixNano())
	}
	if anchor, want := chargeWindowAnchor(t, s, n.RunID, n.NodeID),
		renewed.Add(store.MinBillableSeconds*time.Second).UnixNano(); anchor != want {
		t.Fatalf("charge window = %d, want the minimum from the renewal, %d", anchor, want)
	}

	// safety: fetch and compile run between the renewal and the execution
	// start, so the execution start must leave the window where it is.
	setup := renewed.Add((store.MinBillableSeconds + 10) * time.Second)
	if res, err := store.ChargeNodeCreditsForTest(ctx, s, n.RunID, n.NodeID, claimant.TokenPrefix, setup); err != nil ||
		res.Charge == nil || res.Charge.Seconds != 10 {
		t.Fatalf("setup charge = %+v, %v; want the ten seconds past the minimum", res.Charge, err)
	}
	before := chargeWindowAnchor(t, s, n.RunID, n.NodeID)
	acknowledgeClaimedExecution(t, s, claimant, n)
	if after := chargeWindowAnchor(t, s, n.RunID, n.NodeID); after != before {
		t.Fatalf("the execution start moved a billing window from %d to %d, forgiving the setup", before, after)
	}
	if _, err := s.FinalizeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, setup); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got, want := mustBalance(t, s), granted-(store.MinBillableSeconds+10)*unpinnedNodeRateMicro; got != want {
		t.Fatalf("balance = %d, want the minimum and ten seconds from the renewal billed, %d", got, want)
	}
}
