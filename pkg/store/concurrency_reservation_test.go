package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConcurrencyReservation_EligibilityDoesNotGrantGlobalCapacity(t *testing.T) {
	s := openStore(t)
	ctx := ctxT(t)
	key := "global:deploy"
	acquireT(t, s, store.AcquireSlotRequest{
		Key: key, HolderID: "holder/node", RunID: "holder", NodeID: "node",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	createLiveRunT(t, s, "first")
	createLiveRunT(t, s, "second")

	first, err := s.ReserveConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: key, HolderID: "first/node", RunID: "first", NodeID: "node",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	second, err := s.ReserveConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: key, HolderID: "second/node", RunID: "second", NodeID: "node",
		Capacity: 1, Cost: 1, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("reserve second: %v", err)
	}
	if first.ArrivedAt.IsZero() || !second.ArrivedAt.After(first.ArrivedAt) {
		t.Fatalf("reservation order = %v then %v, want stable increasing arrival", first.ArrivedAt, second.ArrivedAt)
	}

	if _, err := s.ReleaseConcurrencySlot(ctx, key, "holder/node", "success", "", "", 0); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	eligible, err := s.ObserveConcurrencyReservation(ctx, first.Ticket)
	if err != nil {
		t.Fatalf("observe first: %v", err)
	}
	if !eligible.Eligible {
		t.Fatalf("first reservation = %+v, want eligible", eligible)
	}
	blocked, err := s.ObserveConcurrencyReservation(ctx, second.Ticket)
	if err != nil {
		t.Fatalf("observe second: %v", err)
	}
	if blocked.Eligible {
		t.Fatalf("second reservation = %+v, want FIFO blocked", blocked)
	}
	state, err := s.GetConcurrencyState(ctx, key)
	if err != nil {
		t.Fatalf("state before claim: %v", err)
	}
	if len(state.Holders) != 0 {
		t.Fatalf("eligibility granted global capacity: %+v", state.Holders)
	}

	claimed, err := s.ClaimConcurrencyReservation(ctx, first.Ticket, time.Minute)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	if claimed.Kind != store.AcquireGranted || claimed.HolderID != "first/node" {
		t.Fatalf("claim = %+v, want first holder grant", claimed)
	}
}
