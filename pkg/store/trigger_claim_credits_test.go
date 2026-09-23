package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func pendingTrigger(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: id, Pipeline: "build", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// A metered trigger claim starts a whole run, so a team whose balance cannot
// pay for one node's first minute cannot start one on a metered pool.
func TestMeteredTriggerClaimNeedsTheMinimumReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pendingTrigger(t, st, "run-1")
	pool := meteredClaimant(t, st, "agent:cloud")

	_, err := st.ClaimNextTriggerFor(ctx, pool, 0, nil, nil)
	var shortfall *store.InsufficientCreditsError
	if !errors.As(err, &shortfall) || shortfall.RunID != "run-1" || shortfall.RequiredMicro <= 0 {
		t.Fatalf("metered claim on an empty balance = %v, want an insufficient-credits refusal naming run-1", err)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-1", pool, 0); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("metered claim of run-1 by id on an empty balance = %v, want ErrInsufficientCredits", err)
	}
	if tr, err := st.GetTrigger(ctx, "run-1"); err != nil || tr.Status != "pending" {
		t.Fatalf("trigger = %+v, %v; want it left pending", tr, err)
	}

	// Control: an unmetered credential on the same empty balance claims it.
	local := unmeteredClaimant(t, st, "agent:laptop")
	pendingTrigger(t, st, "run-2")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-2", local, 0); err != nil {
		t.Fatalf("unmetered claim: %v", err)
	}

	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, shortfall.RequiredMicro, "", "operator"); err != nil {
		t.Fatal(err)
	}
	tr, err := st.ClaimNextTriggerFor(ctx, pool, 0, nil, nil)
	if err != nil || tr.ID != "run-1" {
		t.Fatalf("metered claim once the balance covers the reservation = %+v, %v; want run-1", tr, err)
	}
}

func unmeteredClaimant(t *testing.T, s *store.Store, principal string) store.ClaimIdentity {
	t.Helper()
	_, tok, err := s.CreateTokenWith(context.Background(), principal, store.TokenKindRunner,
		[]string{"triggers.claim"}, 0, time.Now(), store.TokenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.ClaimIdentity{Principal: principal, TokenPrefix: tok.Prefix}
}
