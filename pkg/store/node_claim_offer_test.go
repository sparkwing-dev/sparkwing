package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func claimOfferResolver(workerID, reservationID string, base, effective int) store.NodeClaimResolver {
	return store.NodeClaimResolverFunc(func(*store.Node) (store.NodeClaimResolution, bool) {
		return store.NodeClaimResolution{
			WorkerID:          workerID,
			ExecutorKind:      "direct",
			ReservationID:     reservationID,
			BasePriority:      base,
			EffectivePriority: effective,
		}, true
	})
}

func offerClaim(t *testing.T, s *store.Store, claimant store.ClaimIdentity, holder, runID, nodeID string, resolver store.NodeClaimResolver) store.NodeClaimOfferResult {
	t.Helper()
	result, err := s.OfferNodeClaim(context.Background(), claimant, holder, runID, nodeID, time.Minute, resolver)
	if err != nil {
		t.Fatalf("OfferNodeClaim: %v", err)
	}
	return result
}

func TestNodeClaimOffer_PrepareCarriesAdmissionDemandAndSkipsEveryIneligibleNode(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	if err := s.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := range 65 {
		if err := s.CreateNode(ctx, store.Node{
			RunID: "run-1", NodeID: fmt.Sprintf("gpu-%02d", i), Status: "pending", NeedsLabels: []string{"gpu"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, "run-1", fmt.Sprintf("gpu-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "cpu", Status: "pending",
		NeedsLabels: []string{"linux"}, PrefersLabels: []string{"workstation"},
		RequestedCores: 2.5, RequestedMemoryBytes: 3 << 30, RequestedSlots: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-1", "cpu"); err != nil {
		t.Fatal(err)
	}

	resolver := store.NodeClaimResolverFunc(func(n *store.Node) (store.NodeClaimResolution, bool) {
		return store.NodeClaimResolution{}, n.NodeID == "cpu"
	})
	summary, err := s.PrepareNextNodeClaim(ctx, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if summary.NodeID != "cpu" || summary.RequestedCores != 2.5 || summary.RequestedMemoryBytes != 3<<30 || summary.RequestedSlots != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if fmt.Sprint(summary.NeedsLabels) != "[linux]" || fmt.Sprint(summary.PrefersLabels) != "[workstation]" {
		t.Fatalf("summary labels = requires %v prefers %v", summary.NeedsLabels, summary.PrefersLabels)
	}
}

func TestNodeClaimOffer_AwardsImmediatelyAtRecordedCeiling(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReadyWithPriorityCeiling(ctx, "run-1", "node-a", 80); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	result := offerClaim(t, s, claimant, "holder-a", "run-1", "node-a", claimOfferResolver("worker-a", "reservation-a", 70, 80))
	if result.Node == nil || result.Pending {
		t.Fatalf("result = %+v, want immediate award", result)
	}
	if result.Node.ClaimPriority != 80 || result.Node.ClaimBasePriority != 70 || result.Node.ClaimReservationID != "reservation-a" {
		t.Fatalf("claim metadata = %+v", result.Node)
	}
}

func TestNodeClaimOffer_DeadlineAwardsPriorityThenEarliestStableIdentity(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReadyWithPriorityCeiling(ctx, "run-1", "node-a", 100); err != nil {
		t.Fatal(err)
	}
	low := store.ClaimIdentity{Principal: "low", TokenPrefix: "swr_low"}
	high := store.ClaimIdentity{Principal: "high", TokenPrefix: "swr_high"}
	if got := offerClaim(t, s, low, "holder-low", "run-1", "node-a", claimOfferResolver("worker-low", "reservation-low", 20, 20)); !got.Pending {
		t.Fatalf("low offer = %+v, want pending", got)
	}
	if got := offerClaim(t, s, high, "holder-high", "run-1", "node-a", claimOfferResolver("worker-high", "reservation-high", 80, 80)); !got.Pending {
		t.Fatalf("high offer = %+v, want pending", got)
	}
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`,
		time.Now().Add(-6*time.Second).UnixNano(), "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if got := offerClaim(t, s, low, "holder-low", "run-1", "node-a", claimOfferResolver("worker-low", "reservation-low", 20, 20)); got.Node != nil || got.Pending {
		t.Fatalf("losing deadline offer = %+v", got)
	}
	got := offerClaim(t, s, high, "holder-high", "run-1", "node-a", claimOfferResolver("worker-high", "reservation-high", 80, 80))
	if got.Node == nil || got.Node.ClaimedBy != "holder-high" {
		t.Fatalf("lost response recovery = %+v", got)
	}
}

func TestNodeClaimOffer_EqualOffersUseStableWorkerIdentity(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node-a")
	if err := s.MarkNodeReady(ctx, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	claimantZ := store.ClaimIdentity{Principal: "runner-z", TokenPrefix: "swr_z"}
	claimantA := store.ClaimIdentity{Principal: "runner-a", TokenPrefix: "swr_a"}
	if got := offerClaim(t, s, claimantZ, "holder-z", "run-1", "node-a", claimOfferResolver("worker-z", "reservation-z", 50, 50)); !got.Pending {
		t.Fatalf("first offer = %+v", got)
	}
	if got := offerClaim(t, s, claimantA, "holder-a", "run-1", "node-a", claimOfferResolver("worker-a", "reservation-a", 50, 50)); !got.Pending {
		t.Fatalf("second offer = %+v", got)
	}
	stamp := time.Now().Add(-6 * time.Second).UnixNano()
	if _, err := s.DB().Exec(`UPDATE node_claim_offers SET offered_at = ? WHERE run_id = ? AND node_id = ?`, stamp, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = ? AND node_id = ?`, stamp, "run-1", "node-a"); err != nil {
		t.Fatal(err)
	}
	if revoked, err := s.FinalizeNodeReady(ctx, "run-1", "node-a"); err != nil || revoked {
		t.Fatalf("FinalizeNodeReady = %v, %v", revoked, err)
	}
	n, err := s.GetNode(ctx, "run-1", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if n.ClaimWorkerID != "worker-a" || n.ClaimedBy != "holder-a" {
		t.Fatalf("winner = worker %q holder %q", n.ClaimWorkerID, n.ClaimedBy)
	}
}

func TestNodeClaimOffer_LostResponseDoesNotClaimAnotherNode(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "first")
	seedRunAndNode(t, s, "run-2", "second")
	if err := s.MarkNodeReady(ctx, "run-1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run-2", "second"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	resolver := claimOfferResolver("worker", "reservation", 100, 100)
	first := offerClaim(t, s, claimant, "holder", "run-1", "first", resolver)
	if first.Node == nil {
		t.Fatal("first offer was not awarded")
	}
	impostor := offerClaim(t, s,
		store.ClaimIdentity{Principal: "other", TokenPrefix: "swr_other"},
		"holder", "run-1", "first", claimOfferResolver("worker", "reservation", 100, 100))
	if impostor.Node != nil || impostor.Pending {
		t.Fatalf("impostor recovered another principal's award: %+v", impostor)
	}
	retry := offerClaim(t, s, claimant, "holder", "run-2", "second", resolver)
	if retry.Node == nil || retry.Node.RunID != "run-1" || retry.Node.NodeID != "first" {
		t.Fatalf("retry = %+v, want original award", retry)
	}
	second, err := s.GetNode(ctx, "run-2", "second")
	if err != nil {
		t.Fatal(err)
	}
	if second.ClaimedBy != "" {
		t.Fatalf("second node claimed by %q", second.ClaimedBy)
	}
}

func TestNodeClaimOffer_FinalizationAndMaximumOfferHaveOneWinner(t *testing.T) {
	for i := range 20 {
		s := newStoreT(t)
		ctx := context.Background()
		runID := fmt.Sprintf("run-%d", i)
		seedRunAndNode(t, s, runID, "node")
		if err := s.MarkNodeReady(ctx, runID, "node"); err != nil {
			t.Fatal(err)
		}
		claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
		start := make(chan struct{})
		offered := make(chan store.NodeClaimOfferResult, 1)
		offerErr := make(chan error, 1)
		go func() {
			<-start
			result, err := s.OfferNodeClaim(ctx, claimant, "holder", runID, "node", time.Minute,
				claimOfferResolver("worker", "reservation", 100, 100))
			offered <- result
			offerErr <- err
		}()
		finalized := make(chan bool, 1)
		finalizeErr := make(chan error, 1)
		go func() {
			<-start
			revoked, err := s.FinalizeNodeReady(ctx, runID, "node")
			finalized <- revoked
			finalizeErr <- err
		}()
		close(start)
		result, revoked := <-offered, <-finalized
		if err := <-offerErr; err != nil {
			t.Fatal(err)
		}
		if err := <-finalizeErr; err != nil {
			t.Fatal(err)
		}
		if (result.Node != nil) == revoked {
			t.Fatalf("iteration %d: award=%v revoked=%v", i, result.Node != nil, revoked)
		}
		n, err := s.GetNode(ctx, runID, "node")
		if err != nil {
			t.Fatal(err)
		}
		if result.Node != nil && n.ClaimedBy != "holder" {
			t.Fatalf("iteration %d: persisted claim = %+v", i, n)
		}
		if revoked && n.ReadyAt != nil {
			t.Fatalf("iteration %d: fallback did not revoke readiness", i)
		}
	}
}

func TestNodeClaimOffer_FinalizationIgnoresExpiredOffers(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	seedRunAndNode(t, s, "run-1", "node")
	if err := s.MarkNodeReady(ctx, "run-1", "node"); err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if got := offerClaim(t, s, claimant, "holder", "run-1", "node", claimOfferResolver("worker", "reservation", 50, 50)); !got.Pending {
		t.Fatalf("offer = %+v", got)
	}
	if _, err := s.DB().Exec(`UPDATE node_claim_offers SET last_seen_at = ?, lease_ns = ?`,
		time.Now().Add(-2*time.Minute).UnixNano(), int64(time.Minute)); err != nil {
		t.Fatal(err)
	}
	revoked, err := s.FinalizeNodeReady(ctx, "run-1", "node")
	if err != nil || !revoked {
		t.Fatalf("FinalizeNodeReady = %v, %v; want fallback", revoked, err)
	}
	var offers int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM node_claim_offers`).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 0 {
		t.Fatalf("expired offers = %d, want 0", offers)
	}
}
