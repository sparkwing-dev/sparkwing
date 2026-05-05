package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// newStoreT opens a fresh store in t.TempDir. Used by every lease
// test so they run in isolation.
func newStoreT(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedPending(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.CreateTrigger(context.Background(), store.Trigger{
		ID:        id,
		Pipeline:  "demo",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
}

func TestLease_ClaimSetsExpiry(t *testing.T) {
	s := newStoreT(t)
	seedPending(t, s, "trig-a")

	got, err := s.ClaimNextTrigger(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("ClaimNextTrigger: %v", err)
	}
	if got.LeaseExpiresAt == nil {
		t.Fatal("lease_expires_at should be set on claim")
	}
	delta := time.Until(*got.LeaseExpiresAt)
	if delta < 4*time.Second || delta > 6*time.Second {
		t.Errorf("lease TTL %v not ~5s", delta)
	}
}

func TestLease_HeartbeatExtends(t *testing.T) {
	s := newStoreT(t)
	seedPending(t, s, "trig-b")

	claimed, err := s.ClaimNextTrigger(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	before := *claimed.LeaseExpiresAt

	// Heartbeat with a longer lease; expiry must move forward.
	time.Sleep(20 * time.Millisecond)
	if _, err := s.HeartbeatTrigger(context.Background(), claimed.ID, 10*time.Second); err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}

	got, err := s.GetTrigger(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.After(before) {
		t.Errorf("lease didn't advance: before=%v after=%v", before, got.LeaseExpiresAt)
	}
}

func TestLease_HeartbeatMissingReturnsNotFound(t *testing.T) {
	s := newStoreT(t)
	_, err := s.HeartbeatTrigger(context.Background(), "nope", 5*time.Second)
	if err != store.ErrNotFound {
		t.Errorf("err=%v want ErrNotFound", err)
	}
}

// TestLease_ReaperRequeuesExpired is the headline crash-recovery
// case: a worker claims, dies silently, and a second worker picks
// up the work.
func TestLease_ReaperRequeuesExpired(t *testing.T) {
	s := newStoreT(t)
	seedPending(t, s, "trig-c")

	// First worker claims with a tiny lease, then "dies" (no
	// heartbeat, no release).
	_, err := s.ClaimNextTrigger(context.Background(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("initial claim: %v", err)
	}
	// Reaper too early: nothing reaped.
	ids, err := s.ReapExpiredTriggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("premature reap: %v", ids)
	}

	// Wait for the lease to expire, then reap.
	time.Sleep(80 * time.Millisecond)
	ids, err = s.ReapExpiredTriggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "trig-c" {
		t.Fatalf("reaped=%v want [trig-c]", ids)
	}

	// Row is back to pending with cleared claim state.
	got, err := s.GetTrigger(context.Background(), "trig-c")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Errorf("status=%q want pending", got.Status)
	}
	if got.ClaimedAt != nil || got.LeaseExpiresAt != nil {
		t.Errorf("claim/lease not cleared: %+v", got)
	}

	// A fresh worker can claim it.
	second, err := s.ClaimNextTrigger(context.Background(), 1*time.Second)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.ID != "trig-c" {
		t.Errorf("second.ID=%q want trig-c", second.ID)
	}
}

// TestLease_ReaperSkipsNonExpired guards against the sweep being
// too aggressive and kicking out still-healthy workers.
func TestLease_ReaperSkipsNonExpired(t *testing.T) {
	s := newStoreT(t)
	seedPending(t, s, "trig-healthy")

	claimed, err := s.ClaimNextTrigger(context.Background(), 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	ids, err := s.ReapExpiredTriggers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("reaped healthy claim: %v", ids)
	}

	// Row remains claimed with its full lease.
	got, err := s.GetTrigger(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "claimed" {
		t.Errorf("status=%q want claimed", got.Status)
	}
}
