package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

// triggerFloor is the minimum a metered trigger claim reserves, read off the
// refusal an empty balance answers with.
func triggerFloor(t *testing.T, st *store.Store, pool store.ClaimIdentity) int64 {
	t.Helper()
	pendingTrigger(t, st, "run-floor")
	_, err := st.ClaimSpecificTriggerFor(context.Background(), "run-floor", pool, 0)
	var shortfall *store.InsufficientCreditsError
	if !errors.As(err, &shortfall) {
		t.Fatalf("claim on an empty balance = %v, want an insufficient-credits refusal", err)
	}
	if shortfall.RequiredMicro%store.MinBillableSeconds != 0 {
		t.Fatalf("floor %d is not a whole minimum of one rate", shortfall.RequiredMicro)
	}
	return shortfall.RequiredMicro
}

func balance(t *testing.T, st *store.Store) int64 {
	t.Helper()
	b, err := st.CreditBalanceMicro(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// backdateTriggerClaim moves a claim's reservation, and its lease when lease
// is non-zero, into the past, standing in for a step that ran that long.
func backdateTriggerClaim(t *testing.T, st *store.Store, id string, reservedAt, lease time.Time) {
	t.Helper()
	query, args := `UPDATE triggers SET credit_reserved_at = ? WHERE id = ?`, []any{reservedAt.UnixNano(), id}
	if !lease.IsZero() {
		query = `UPDATE triggers SET credit_reserved_at = ?, lease_expires_at = ? WHERE id = ?`
		args = []any{reservedAt.UnixNano(), lease.UnixNano(), id}
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, query), args...); err != nil {
		t.Fatal(err)
	}
}

// A metered trigger claim holds its minute on the ledger, so a balance that
// covers one minute starts one run, not one per claim that reads it.
func TestMeteredTriggerClaimReservesItsMinute(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "agent:cloud")
	floor := triggerFloor(t, st, pool)
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, floor, "", "operator"); err != nil {
		t.Fatal(err)
	}
	pendingTrigger(t, st, "run-a")
	pendingTrigger(t, st, "run-b")

	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-a", pool, 0); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if got := balance(t, st); got != 0 {
		t.Fatalf("balance after the claim = %d, want 0: the claim reserved nothing", got)
	}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-b", pool, 0); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("second claim against the reserved minute = %v, want ErrInsufficientCredits", err)
	}
	if tr, err := st.GetTrigger(ctx, "run-b"); err != nil || tr.Status != "pending" {
		t.Fatalf("refused trigger = %+v, %v; want it left pending", tr, err)
	}

	// Control: an unmetered credential takes the same trigger on the spent balance.
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-b", unmeteredClaimant(t, st, "agent:laptop"), 0); err != nil {
		t.Fatalf("unmetered claim: %v", err)
	}
	if got := balance(t, st); got != 0 {
		t.Fatalf("balance after an unmetered claim = %d, want 0", got)
	}
}

func TestDisabledMeteringDoesNotSettleAnEarlierTriggerReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "agent:cloud")
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 10*triggerFloor(t, st, pool), "", "operator"); err != nil {
		t.Fatal(err)
	}
	pendingTrigger(t, st, "run-licensed")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-licensed", pool, 0); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListCreditCharges(ctx, 10)
	if err != nil || len(before) != 1 {
		t.Fatalf("earlier reservation = %+v, %v", before, err)
	}
	if err := st.FinishTrigger(store.WithoutCreditMetering(ctx), "run-licensed"); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListCreditCharges(ctx, 10)
	if err != nil || len(after) != len(before) {
		t.Fatalf("charges after unlicensed finish = %+v, %v", after, err)
	}
}

// Postgres runs each claim in its own transaction, so the reservation has to
// serialize on the ledger: claims racing for a balance that covers one minute
// admit exactly one trigger.
func TestMeteredTriggerClaimsRaceForOneMinute(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "agent:cloud")
	floor := triggerFloor(t, st, pool)
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, floor, "", "operator"); err != nil {
		t.Fatal(err)
	}
	const claimers = 8
	for i := range 2 * claimers {
		pendingTrigger(t, st, fmt.Sprintf("run-%02d", i))
	}
	start := make(chan struct{})
	errs := make([]error, claimers)
	var wg sync.WaitGroup
	for i := range claimers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = st.ClaimNextTriggerFor(ctx, pool, 0, []string{"build"}, nil)
		}()
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, store.ErrInsufficientCredits):
		default:
			t.Fatalf("claim: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent metered claims won a balance covering one minute, want exactly 1", won, claimers)
	}
	if got := balance(t, st); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
}

// The trigger step is billed by the wall time its claim ran: a finish inside
// the reserved minimum pays the minimum, and one past it bills the rest.
func TestTriggerFinishSettlesItsReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "agent:cloud")
	floor := triggerFloor(t, st, pool)
	rate := floor / store.MinBillableSeconds
	grant := 10 * floor
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, grant, "", "operator"); err != nil {
		t.Fatal(err)
	}

	pendingTrigger(t, st, "run-short")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-short", pool, 0); err != nil {
		t.Fatal(err)
	}
	backdateTriggerClaim(t, st, "run-short", time.Now().Add(-5*time.Second), time.Time{})
	if err := st.FinishTrigger(ctx, "run-short"); err != nil {
		t.Fatal(err)
	}
	spent := grant - balance(t, st)
	if spent != floor {
		t.Fatalf("a 5s trigger step cost %d, want the %ds minimum at %d/s, %d", spent, store.MinBillableSeconds, rate, floor)
	}
	if err := st.FinishTrigger(ctx, "run-short"); err != nil {
		t.Fatal(err)
	}
	if again := grant - balance(t, st); again != spent {
		t.Fatalf("a second finish moved the ledger from %d to %d", spent, again)
	}

	pendingTrigger(t, st, "run-long")
	before := balance(t, st)
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-long", pool, 0); err != nil {
		t.Fatal(err)
	}
	backdateTriggerClaim(t, st, "run-long", time.Now().Add(-150*time.Second), time.Time{})
	if _, err := st.FinishTriggerAtGeneration(ctx, "run-long", 99); err != nil {
		t.Fatal(err)
	}
	if got := before - balance(t, st); got != floor {
		t.Fatalf("a finish from a superseded generation settled the claim: spent %d, want the reserved %d", got, floor)
	}
	if err := st.FinishTrigger(ctx, "run-long"); err != nil {
		t.Fatal(err)
	}
	if got := before - balance(t, st); got < 150*rate || got > 151*rate {
		t.Fatalf("a 150s trigger step cost %d, want 150s at %d/s", got, rate)
	}
}

// A lapsed claim is billed through its lease's end, not through the reap, and
// a claim that never started its run is refunded whole.
func TestTriggerReapAndRequeueSettleTheReservation(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	pool := meteredClaimant(t, st, "agent:cloud")
	floor := triggerFloor(t, st, pool)
	rate := floor / store.MinBillableSeconds
	if _, err := st.GrantCredits(ctx, store.CreditGrantFree, 10*floor, "", "operator"); err != nil {
		t.Fatal(err)
	}

	pendingTrigger(t, st, "run-lapsed")
	before := balance(t, st)
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-lapsed", pool, 0); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	backdateTriggerClaim(t, st, "run-lapsed", now.Add(-10*time.Minute), now.Add(-10*time.Minute+90*time.Second))
	ids, err := store.Maintenance.ReapExpiredTriggers(st, ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("reap = %v, %v; want run-lapsed", ids, err)
	}
	if got := before - balance(t, st); got != 90*rate {
		t.Fatalf("a claim whose lease lapsed 90s in cost %d, want %d", got, 90*rate)
	}

	pendingTrigger(t, st, "run-unstarted")
	before = balance(t, st)
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-unstarted", pool, 0); err != nil {
		t.Fatal(err)
	}
	backdateTriggerClaim(t, st, "run-unstarted", time.Now().Add(-5*time.Minute), time.Time{})
	if ok, err := st.RequeueUnstartedClaim(ctx, "run-unstarted"); err != nil || !ok {
		t.Fatalf("requeue = %v, %v", ok, err)
	}
	if got := before - balance(t, st); got != 0 {
		t.Fatalf("a claim that never started its run cost %d, want a full refund", got)
	}
}
