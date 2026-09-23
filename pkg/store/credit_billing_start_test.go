package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// Billing runs from the moment the machine that executes a node starts work
// on it to the node's finish, so fetch and compile are billed. A runner that
// claims its own work starts at the claim; a dispatcher claims before the pod
// exists, so its node starts at the pod's first claim renewal or execution
// start, whichever comes first, and provisioning is never billed.

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
	if _, err := s.GrantCredits(context.Background(), store.CreditGrantPaid, granted, "pay_"+runID, "admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	return claimant, granted
}

func dispatchedClaim(t *testing.T, s *store.Store, claimant store.ClaimIdentity, runID string) *store.Node {
	t.Helper()
	n, err := s.ClaimNamedNode(context.Background(), claimant, runID, "build", "k8s-job:"+runID, time.Minute,
		store.NamedClaimOptions{SizesToClass: true})
	if err != nil {
		t.Fatalf("dispatched claim: %v", err)
	}
	return n
}

func mustBalance(t *testing.T, s *store.Store) int64 {
	t.Helper()
	b, err := s.CreditBalanceMicro(context.Background())
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
	res, err := s.ChargeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, renewed)
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
	setup := renewed.Add(30 * time.Second)
	if res, err := s.ChargeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, setup); err != nil ||
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
	if got, want := mustBalance(t, s), granted-30*unpinnedNodeRateMicro; got != want {
		t.Fatalf("balance = %d, want thirty seconds from the renewal billed, %d", got, want)
	}
}

func TestQueueClaimBillsSetupBeforeExecution(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, granted := fundedMeteredNode(t, s, "run-setup")
	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	if from := billingFrom(t, s, n.RunID, n.NodeID); from == 0 {
		t.Fatal("a runner claiming its own work did not start billing at the claim")
	}
	setChargeWindowWithoutExecution(t, s, n.RunID, n.NodeID, time.Now().Add(-25*time.Second))
	res, err := s.ChargeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, time.Now())
	if err != nil || res.Charge == nil || res.Charge.Kind != store.CreditChargeUsage {
		t.Fatalf("setup heartbeat = %+v, %v; want the compile time billed", res.Charge, err)
	}
	setupSeconds := res.Charge.Seconds
	before := chargeWindowAnchor(t, s, n.RunID, n.NodeID)
	acknowledgeClaimedExecution(t, s, claimant, n)
	if after := chargeWindowAnchor(t, s, n.RunID, n.NodeID); after != before {
		t.Fatalf("the execution start moved the window from %d to %d", before, after)
	}
	want := granted - (store.MinBillableSeconds+setupSeconds)*unpinnedNodeRateMicro
	if got := mustBalance(t, s); got != want {
		t.Fatalf("balance = %d, want the minimum and %ds of setup billed, %d", got, setupSeconds, want)
	}
}

// A node the platform failed before its execution started gets back
// everything its claim billed, setup included. A failure of the customer's
// own, such as a compile error or a cancellation, keeps the setup billed, and
// so does a platform failure once execution has started.
func TestSetupRefundFollowsWhoseFailureItWas(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  string
		reason   string
		started  bool
		refunded bool
	}{
		{"agent lost during setup", "failed", store.FailureAgentLost, false, false},
		{"runner lease expired during setup", "failed", store.FailureRunnerLeaseExpired, false, false},
		{"no machine came free", "failed", store.FailureQueueTimeout, false, true},
		{"log service refused the runner", "failed", store.FailureLogsAuth, false, true},
		{"log service dropped the writes", "failed", store.FailureLogsDropped, false, true},
		{"compile error", "failed", store.FailureUnknown, false, false},
		{"out of memory while compiling", "failed", store.FailureOOMKilled, false, false},
		{"cancelled during setup", "cancelled", "", false, false},
		{"agent lost after execution started", "failed", store.FailureAgentLost, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			claimant, granted := fundedMeteredNode(t, s, "run-fail")
			n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
			if err != nil || n == nil {
				t.Fatalf("claim: %v", err)
			}
			setChargeWindowWithoutExecution(t, s, n.RunID, n.NodeID, time.Now().Add(-25*time.Second))
			if res, err := s.ChargeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, time.Now()); err != nil ||
				res.Charge == nil {
				t.Fatalf("setup heartbeat = %+v, %v", res.Charge, err)
			}
			if tc.started {
				acknowledgeClaimedExecution(t, s, claimant, n)
			}
			billed := granted - mustBalance(t, s)
			if billed <= store.MinBillableSeconds*unpinnedNodeRateMicro {
				t.Fatalf("billed %d before the failure, want setup past the minimum", billed)
			}
			if err := s.FinishNodeWithReason(ctx, n.RunID, n.NodeID, tc.outcome, "stopped", nil, tc.reason, nil); err != nil {
				t.Fatalf("finish: %v", err)
			}
			if _, err := s.FinalizeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, time.Now()); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			got := mustBalance(t, s)
			if tc.refunded && got != granted {
				t.Fatalf("balance = %d, want the whole claim refunded to %d", got, granted)
			}
			if !tc.refunded && got >= granted-store.MinBillableSeconds*unpinnedNodeRateMicro {
				t.Fatalf("balance = %d, want the setup kept billed (at most %d)",
					got, granted-store.MinBillableSeconds*unpinnedNodeRateMicro-1)
			}
			if _, err := s.FinalizeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, time.Now()); err != nil {
				t.Fatalf("second finalize: %v", err)
			}
			if again := mustBalance(t, s); again != got {
				t.Fatalf("a second finalize moved the balance from %d to %d", got, again)
			}
		})
	}
}

// A claim reaped before execution started is a machine that ran setup until
// its lease ran out, so the setup it billed stands.
func TestReapBeforeExecutionKeepsTheSetupTheClaimBilled(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	claimant, _ := fundedMeteredNode(t, s, "run-reaped")
	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	setChargeWindowWithoutExecution(t, s, n.RunID, n.NodeID, time.Now().Add(-25*time.Second))
	if res, err := s.ChargeNodeCredits(ctx, n.RunID, n.NodeID, claimant.TokenPrefix, time.Now()); err != nil ||
		res.Charge == nil {
		t.Fatalf("setup heartbeat = %+v, %v", res.Charge, err)
	}
	expireNodeLease(t, s, n.RunID, n.NodeID, time.Now().Add(-time.Minute))
	before := mustBalance(t, s)
	if _, err := s.ReapExpiredNodeClaims(ctx); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if got := mustBalance(t, s); got != before {
		t.Fatalf("balance = %d, want %d: the machine ran setup, so its billing stands", got, before)
	}
}
