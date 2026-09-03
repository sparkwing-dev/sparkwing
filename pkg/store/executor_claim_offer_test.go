package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func enrollOfferExecutor(t *testing.T, s *store.Store, name string, base, ceiling int, capabilities ...string) store.ClaimIdentity {
	t.Helper()
	identity := store.ClaimIdentity{Principal: "principal-" + name, TokenPrefix: "swr_" + name}
	if err := s.EnrollExecutor(context.Background(), identity.TokenPrefix, store.Executor{
		Name: name, Kind: "agent", Location: "local", Principal: identity.Principal,
		Capabilities: capabilities, BasePriority: base, PriorityCeiling: ceiling,
		MaxConcurrent: 2, Budget: store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatalf("EnrollExecutor(%s): %v", name, err)
	}
	if err := s.HeartbeatExecutor(context.Background(), identity, name,
		store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30}, 0, time.Now()); err != nil {
		t.Fatalf("HeartbeatExecutor(%s): %v", name, err)
	}
	return identity
}

func executorOffer(t *testing.T, s *store.Store, identity store.ClaimIdentity, name, holder, reservation, runID, nodeID string, slot int) store.ExecutorClaimOfferResult {
	t.Helper()
	summary, err := s.SchedulingSummary(context.Background(), runID, nodeID)
	if err != nil {
		t.Fatalf("SchedulingSummary: %v", err)
	}
	result, err := s.OfferExecutorClaim(context.Background(), identity, store.ExecutorClaimOffer{
		ExecutorName: name, HolderID: holder, RunID: runID, NodeID: nodeID,
		ReservationID: reservation, ResourceDigest: summary.ResourceDigest, Slot: slot, Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("OfferExecutorClaim: %v", err)
	}
	return result
}

func TestExecutorSelectedEventUsesPublicAttribution(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desktop", 100, 100)
	if err := s.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run", "build"); err != nil {
		t.Fatal(err)
	}
	result := executorOffer(t, s, identity, "desktop", "agent:desktop:0", "reservation-secret", "run", "build", 0)
	if result.Node == nil {
		t.Fatal("offer did not win")
	}
	events, err := s.ListEventsAfter(ctx, "run", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind != "executor_selected" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["executor_name"] != "desktop" || payload["executor_kind"] != "agent" || payload["location"] != "local" {
			t.Fatalf("public attribution = %v", payload)
		}
		for _, internal := range []string{"coordinator_id", "membership_id", "executor_id", "reservation_id", "holder_id", "token_prefix"} {
			if _, exists := payload[internal]; exists {
				t.Fatalf("executor_selected exposed %s: %v", internal, payload)
			}
		}
		return
	}
	t.Fatal("missing executor_selected event")
}

func TestExecutorClaimPreparationScansPastSixtyFourIneligibleNodes(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 50, 80, "linux")
	if err := s.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := range 65 {
		id := fmt.Sprintf("gpu-%02d", i)
		if err := s.CreateNode(ctx, store.Node{RunID: "run", NodeID: id, Status: "pending", NeedsLabels: []string{"gpu"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, "run", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run", NodeID: "linux", Status: "pending", NeedsLabels: []string{"linux"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run", "linux"); err != nil {
		t.Fatal(err)
	}
	preparation, err := s.PrepareNextExecutorClaim(ctx, identity, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Summary.NodeID != "linux" || preparation.Summary.ResourceDigest == "" || !preparation.Membership.Eligible {
		t.Fatalf("preparation = %+v", preparation)
	}
}

func TestExecutorClaimOfferAwardsImmediatelyAtRecordedCeiling(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 80, 80, "linux")
	if err := s.EnrollExecutor(context.Background(), identity.TokenPrefix, store.Executor{
		Name: "desk", Kind: "agent", Location: "local", Principal: identity.Principal,
		Capabilities: []string{"linux"}, BasePriority: 80, PriorityCeiling: 80,
		MaxConcurrent: 1, Budget: store.ExecutorResource{Cores: 8, MemoryBytes: 16 << 30},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, store.Run{ID: "run", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: "run", NodeID: "work", Status: "pending", NeedsLabels: []string{"linux"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReadyWithPriorityCeiling(ctx, "run", "work", 80); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReadyWithPriorityCeiling(ctx, "run", "work", 100); err != nil {
		t.Fatal(err)
	}
	result := executorOffer(t, s, identity, "desk", "holder", "reservation", "run", "work", 0)
	if result.Node == nil || result.Pending || result.Node.ClaimedBy != "holder" {
		t.Fatalf("offer result = %+v", result)
	}
	if result.Node.ClaimBasePriority != 80 || result.Node.ClaimPriority != 80 ||
		result.Node.ClaimWorkerID != "desk" || result.Node.ClaimExecutorKind != "agent" ||
		result.Node.ClaimReservationID != "reservation" {
		t.Fatalf("returned claim metadata = %+v", result.Node)
	}
	var executor, reservation string
	var slot int
	if err := s.DB().QueryRow(`SELECT claim_executor, claim_reservation, claim_slot FROM nodes WHERE run_id = 'run' AND node_id = 'work'`).Scan(&executor, &reservation, &slot); err != nil {
		t.Fatal(err)
	}
	if executor != "desk" || reservation != "reservation" || slot != 0 {
		t.Fatalf("claim binding = %q %q %d", executor, reservation, slot)
	}
	retry := executorOffer(t, s, identity, "desk", "holder", "reservation", "run", "work", 0)
	if retry.Node == nil || retry.Node.ClaimedBy != "holder" {
		t.Fatalf("lost response retry = %+v", retry)
	}
}

func TestExecutorClaimOfferDeadlineUsesPriorityAndRecoversLostWinnerResponse(t *testing.T) {
	s := newStoreT(t)
	low := enrollOfferExecutor(t, s, "low", 20, 20, "linux")
	high := enrollOfferExecutor(t, s, "high", 80, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	if got := executorOffer(t, s, low, "low", "holder-low", "reservation-low", "run", "work", 0); !got.Pending {
		t.Fatalf("low offer = %+v", got)
	}
	if got := executorOffer(t, s, high, "high", "holder-high", "reservation-high", "run", "work", 0); !got.Pending {
		t.Fatalf("high offer = %+v", got)
	}
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = 'run' AND node_id = 'work'`, time.Now().Add(-6*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if got := executorOffer(t, s, low, "low", "holder-low", "reservation-low", "run", "work", 0); got.Node != nil || got.Pending {
		t.Fatalf("losing offer = %+v", got)
	}
	got := executorOffer(t, s, high, "high", "holder-high", "reservation-high", "run", "work", 0)
	if got.Node == nil || got.Node.ClaimedBy != "holder-high" {
		t.Fatalf("lost response recovery = %+v", got)
	}
}

func TestExecutorClaimOfferEqualPriorityUsesEarliestThenStableIdentity(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	first := enrollOfferExecutor(t, s, "zeta", 50, 50, "linux")
	second := enrollOfferExecutor(t, s, "alpha", 50, 50, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	if got := executorOffer(t, s, first, "zeta", "holder-z", "reservation-z", "run", "work", 0); !got.Pending {
		t.Fatalf("first offer = %+v", got)
	}
	time.Sleep(time.Millisecond)
	if got := executorOffer(t, s, second, "alpha", "holder-a", "reservation-a", "run", "work", 0); !got.Pending {
		t.Fatalf("second offer = %+v", got)
	}
	stamp := time.Now().Add(-6 * time.Second).UnixNano()
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = 'run' AND node_id = 'work'`, stamp); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinalizeExecutorClaimRound(ctx, "run", "work")
	if err != nil || result.Revoked || result.Pending {
		t.Fatalf("finalize = %+v, %v", result, err)
	}
	n, err := s.GetNode(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if n.ClaimedBy != "holder-z" {
		t.Fatalf("earliest winner = %q", n.ClaimedBy)
	}

	s2 := newStoreT(t)
	alpha := enrollOfferExecutor(t, s2, "alpha", 50, 50, "linux")
	zeta := enrollOfferExecutor(t, s2, "zeta", 50, 50, "linux")
	seedExecutorNode(t, s2, "run", 1, "linux")
	executorOffer(t, s2, zeta, "zeta", "holder-z", "reservation-z", "run", "work", 0)
	executorOffer(t, s2, alpha, "alpha", "holder-a", "reservation-a", "run", "work", 0)
	stamp = time.Now().Add(-6 * time.Second).UnixNano()
	if _, err := s2.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = 'run' AND node_id = 'work'`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.DB().Exec(`UPDATE node_claim_offers SET offered_at = ?`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.FinalizeExecutorClaimRound(ctx, "run", "work"); err != nil {
		t.Fatal(err)
	}
	n, err = s2.GetNode(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if n.ClaimedBy != "holder-a" {
		t.Fatalf("stable identity winner = %q", n.ClaimedBy)
	}
}

func TestExecutorClaimOfferRejectsWrongCredentialDigestAndReservationReuse(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 40, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	preparation, err := s.PrepareNextExecutorClaim(ctx, identity, "desk")
	if err != nil {
		t.Fatal(err)
	}
	base := store.ExecutorClaimOffer{ExecutorName: "desk", HolderID: "holder", RunID: "run", NodeID: "work", ReservationID: "reservation", ResourceDigest: preparation.Summary.ResourceDigest, Slot: 0, Lease: time.Minute}
	wrong := store.ClaimIdentity{Principal: identity.Principal, TokenPrefix: "swr_other"}
	if _, err := s.OfferExecutorClaim(ctx, wrong, base); !errors.Is(err, store.ErrExecutorCredentialMismatch) {
		t.Fatalf("wrong credential = %v", err)
	}
	badDigest := base
	badDigest.ResourceDigest = "sha256:wrong"
	if _, err := s.OfferExecutorClaim(ctx, identity, badDigest); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("wrong digest = %v", err)
	}
	if result, err := s.OfferExecutorClaim(ctx, identity, base); err != nil || !result.Pending {
		t.Fatalf("valid offer = %+v, %v", result, err)
	}
	reused := base
	reused.HolderID = "other-holder"
	reused.Slot = 1
	if _, err := s.OfferExecutorClaim(ctx, identity, reused); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("reused reservation = %v", err)
	}
}

func TestExecutorClaimPreparationUsesOneExecutorSlotPerNode(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 40, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	if err := s.CreateNode(ctx, store.Node{RunID: "run", NodeID: "work-2", Status: "pending", NeedsLabels: []string{"linux"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNodeReady(ctx, "run", "work-2"); err != nil {
		t.Fatal(err)
	}
	first, err := s.PrepareNextExecutorClaim(ctx, identity, "desk")
	if err != nil {
		t.Fatal(err)
	}
	offer := store.ExecutorClaimOffer{
		ExecutorName: "desk", HolderID: "holder-0", RunID: "run", NodeID: first.Summary.NodeID,
		ReservationID: "reservation-0", ResourceDigest: first.Summary.ResourceDigest, Slot: 0, Lease: time.Minute,
	}
	if result, err := s.OfferExecutorClaim(ctx, identity, offer); err != nil || !result.Pending {
		t.Fatalf("first offer = %+v, %v", result, err)
	}
	duplicate := offer
	duplicate.HolderID = "holder-1"
	duplicate.ReservationID = "reservation-1"
	duplicate.Slot = 1
	if _, err := s.OfferExecutorClaim(ctx, identity, duplicate); !errors.Is(err, store.ErrLockHeld) {
		t.Fatalf("second slot on same node = %v", err)
	}
	second, err := s.PrepareNextExecutorClaim(ctx, identity, "desk")
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.NodeID != "work-2" {
		t.Fatalf("next prepared node = %q", second.Summary.NodeID)
	}
}

func TestExecutorClaimExpiredOfferCanBeReplaced(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 40, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	if got := executorOffer(t, s, identity, "desk", "old-holder", "old-reservation", "run", "work", 0); !got.Pending {
		t.Fatalf("old offer = %+v", got)
	}
	if _, err := s.DB().Exec(`UPDATE node_claim_offers SET last_seen_at = ?`, time.Now().Add(-3*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	preparation, err := s.PrepareNextExecutorClaim(ctx, identity, "desk")
	if err != nil || preparation.Summary.NodeID != "work" {
		t.Fatalf("replacement preparation = %+v, %v", preparation, err)
	}
	result, err := s.OfferExecutorClaim(ctx, identity, store.ExecutorClaimOffer{
		ExecutorName: "desk", HolderID: "new-holder", RunID: "run", NodeID: "work",
		ReservationID: "new-reservation", ResourceDigest: preparation.Summary.ResourceDigest, Slot: 0, Lease: time.Minute,
	})
	if err != nil || !result.Pending {
		t.Fatalf("replacement offer = %+v, %v", result, err)
	}
	var count int
	var holder string
	if err := s.DB().QueryRow(`SELECT COUNT(*), MAX(holder_id) FROM node_claim_offers`).Scan(&count, &holder); err != nil {
		t.Fatal(err)
	}
	if count != 1 || holder != "new-holder" {
		t.Fatalf("persisted offers = %d, holder %q", count, holder)
	}
}

func TestExecutorClaimFinalizationIgnoresExpiredOffersAndTransfersFallbackOnce(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 50, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	if got := executorOffer(t, s, identity, "desk", "holder", "reservation", "run", "work", 0); !got.Pending {
		t.Fatalf("offer = %+v", got)
	}
	stamp := time.Now().Add(-6 * time.Second).UnixNano()
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = 'run' AND node_id = 'work'`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE node_claim_offers SET last_seen_at = ?`, time.Now().Add(-3*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinalizeExecutorClaimRound(ctx, "run", "work")
	if err != nil || !result.Revoked || result.Pending {
		t.Fatalf("finalize = %+v, %v", result, err)
	}
	again, err := s.FinalizeExecutorClaimRound(ctx, "run", "work")
	if err != nil || again.Revoked || again.Pending {
		t.Fatalf("second finalize = %+v, %v", again, err)
	}
	var offers int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM node_claim_offers`).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 0 {
		t.Fatalf("offers after fallback = %d", offers)
	}
}

func TestExecutorClaimOfferAndFallbackHaveOneWinner(t *testing.T) {
	s := newStoreT(t)
	ctx := context.Background()
	identity := enrollOfferExecutor(t, s, "desk", 50, 80, "linux")
	seedExecutorNode(t, s, "run", 1, "linux")
	summary, err := s.SchedulingSummary(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE nodes SET offer_started_at = ? WHERE run_id = 'run' AND node_id = 'work'`, time.Now().Add(-6*time.Second).UnixNano()); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var offerResult store.ExecutorClaimOfferResult
	var offerErr error
	var fallback store.ExecutorClaimRoundResult
	var fallbackErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		offerResult, offerErr = s.OfferExecutorClaim(ctx, identity, store.ExecutorClaimOffer{
			ExecutorName: "desk", HolderID: "holder", RunID: "run", NodeID: "work",
			ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0, Lease: time.Minute,
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		fallback, fallbackErr = s.FinalizeExecutorClaimRound(ctx, "run", "work")
	}()
	close(start)
	wg.Wait()
	if offerErr != nil {
		t.Fatalf("offer: %v", offerErr)
	}
	if fallbackErr != nil {
		t.Fatalf("fallback: %v", fallbackErr)
	}
	if (offerResult.Node != nil) == fallback.Revoked {
		t.Fatalf("offer = %+v, fallback = %+v; want exactly one authority", offerResult, fallback)
	}
	n, err := s.GetNode(ctx, "run", "work")
	if err != nil {
		t.Fatal(err)
	}
	if offerResult.Node != nil && n.ClaimedBy != "holder" {
		t.Fatalf("awarded node holder = %q", n.ClaimedBy)
	}
	if fallback.Revoked && (n.ClaimedBy != "" || n.ReadyAt != nil) {
		t.Fatalf("fallback node = %+v", n)
	}
}
