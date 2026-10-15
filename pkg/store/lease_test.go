package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

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
	s := storetest.Open(t)
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
	s := storetest.Open(t)
	seedPending(t, s, "trig-b")

	claimed, err := s.ClaimNextTrigger(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	before := *claimed.LeaseExpiresAt

	if _, err := s.HeartbeatTrigger(context.Background(), claimed.ID, 10*time.Second); err != nil {
		t.Fatalf("HeartbeatTrigger: %v", err)
	}

	got, err := s.GetTrigger(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaseExpiresAt == nil || got.LeaseExpiresAt.Sub(before) < 9*time.Second {
		t.Errorf("lease did not extend by at least 9s: before=%v after=%v", before, got.LeaseExpiresAt)
	}
}

func TestLease_HeartbeatMissingReturnsNotFound(t *testing.T) {
	s := storetest.Open(t)
	_, err := s.HeartbeatTrigger(context.Background(), "nope", 5*time.Second)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err=%v want ErrNotFound", err)
	}
}
