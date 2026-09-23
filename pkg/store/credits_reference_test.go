package store_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func grantCount(t *testing.T, s *store.Store) int {
	t.Helper()
	grants, err := s.ListCreditGrants(context.Background(), 100)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	return len(grants)
}

// A payment webhook redelivers until it sees a 2xx, so the second delivery of
// one payment must answer with the grant the first one wrote.
func TestGrantCreditsIsIdempotentByReference(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	first, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent,
		Reference: "pi_1", CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if !first.Created {
		t.Fatal("the first grant reports that it wrote nothing")
	}
	second, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent,
		Reference: "pi_1", CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("redelivered grant: %v", err)
	}
	if second.Created {
		t.Fatal("the redelivered payment wrote a second grant")
	}
	if second.Grant.ID != first.Grant.ID {
		t.Fatalf("redelivery returned %q, want the first grant %q", second.Grant.ID, first.Grant.ID)
	}
	if n := grantCount(t, s); n != 1 {
		t.Fatalf("grants = %d, want the one row", n)
	}
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(1000 * store.MicroCreditsPerCent); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}
}

func TestGrantCreditsKeepsKindsWithOneReferenceApart(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 5*store.MicroCreditsPerCent,
		"promo_7", "admin"); err != nil {
		t.Fatalf("free grant: %v", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 20*store.MicroCreditsPerCent,
		"promo_7", "admin"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	if n := grantCount(t, s); n != 2 {
		t.Fatalf("grants = %d, want one of each kind", n)
	}
}

func TestGrantCreditsRepeatsAnEmptyReference(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	for range 2 {
		if _, err := s.GrantCredits(ctx, store.CreditGrantFree,
			5*store.MicroCreditsPerCent, "", "admin"); err != nil {
			t.Fatalf("unreferenced grant: %v", err)
		}
	}
	if n := grantCount(t, s); n != 2 {
		t.Fatalf("grants = %d, want both unreferenced rows", n)
	}
}

func TestCreditReversalNetsOutOfTheBalanceAndTheHistory(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCent, "pi_2", "billing"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	res, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -400 * store.MicroCreditsPerCent,
		Reference: "re_2a", Reverses: "pi_2", CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("reversal: %v", err)
	}
	if !res.Created || res.Grant.Reverses != "pi_2" {
		t.Fatalf("reversal row = %+v", res)
	}

	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(600 * store.MicroCreditsPerCent); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	state, err := s.CreditState(ctx, 0)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if want := int64(1000 * store.MicroCreditsPerCent); state.GrantedMicro != want {
		t.Errorf("granted = %d, want the paid grant %d", state.GrantedMicro, want)
	}
	if want := int64(400 * store.MicroCreditsPerCent); state.ReversedMicro != want {
		t.Errorf("reversed = %d, want %d", state.ReversedMicro, want)
	}
	if want := int64(600 * store.MicroCreditsPerCent); state.BalanceMicro != want {
		t.Errorf("state balance = %d, want %d", state.BalanceMicro, want)
	}

	totals, err := s.CreditLedgerTotals(ctx)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if want := int64(400 * store.MicroCreditsPerCent); totals.ReversedMicro != want {
		t.Errorf("totals reversed = %d, want %d", totals.ReversedMicro, want)
	}
	if want := int64(600 * store.MicroCreditsPerCent); totals.BalanceMicro != want {
		t.Errorf("totals balance = %d, want %d", totals.BalanceMicro, want)
	}

	grants, err := s.ListCreditGrants(ctx, 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("history rows = %d, want the payment and its reversal", len(grants))
	}
	if grants[0].Kind != store.CreditGrantReversal || grants[0].Reverses != "pi_2" {
		t.Fatalf("newest history row = %+v, want the reversal of pi_2", grants[0])
	}
	if grants[0].AmountMicro >= 0 {
		t.Fatalf("reversal amount = %d, want a negative row", grants[0].AmountMicro)
	}
}

// Partial refunds of one payment each carry their own refund id, and a
// redelivered refund answers with the reversal already written.
func TestCreditReversalIsIdempotentByRefundReference(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCent, "pi_3", "billing"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	partial := store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -100 * store.MicroCreditsPerCent,
		Reference: "re_3a", Reverses: "pi_3", CreatedBy: "billing",
	}
	first, err := s.RecordCreditGrant(ctx, partial)
	if err != nil {
		t.Fatalf("first refund: %v", err)
	}
	again, err := s.RecordCreditGrant(ctx, partial)
	if err != nil {
		t.Fatalf("redelivered refund: %v", err)
	}
	if again.Created || again.Grant.ID != first.Grant.ID {
		t.Fatalf("redelivered refund = %+v, want the first reversal", again)
	}
	second := partial
	second.Reference = "re_3b"
	if _, err := s.RecordCreditGrant(ctx, second); err != nil {
		t.Fatalf("second partial refund: %v", err)
	}
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(800 * store.MicroCreditsPerCent); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}
}

func TestCreditReversalRefusesAPaymentTheLedgerNeverSaw(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantFree,
		10*store.MicroCreditsPerCent, "pi_absent", "admin"); err != nil {
		t.Fatalf("free grant: %v", err)
	}
	_, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -10 * store.MicroCreditsPerCent,
		Reference: "re_absent", Reverses: "pi_absent", CreatedBy: "billing",
	})
	if err == nil {
		t.Fatal("a reversal of a reference no paid grant carries was accepted")
	}
	if !strings.Contains(err.Error(), "pi_absent") {
		t.Fatalf("refusal = %v, want it to name the reference", err)
	}
	if n := grantCount(t, s); n != 1 {
		t.Fatalf("grants = %d, want the refused reversal to have written nothing", n)
	}
}

func TestCreditGrantRequestValidation(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		1000*store.MicroCreditsPerCent, "pi_4", "billing"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}

	for name, req := range map[string]store.CreditGrantRequest{
		"a positive reversal": {
			Kind: store.CreditGrantReversal, AmountMicro: 5, Reference: "re_4", Reverses: "pi_4",
		},
		"a reversal with no refund id": {
			Kind: store.CreditGrantReversal, AmountMicro: -5, Reverses: "pi_4",
		},
		"a reversal that names no payment": {
			Kind: store.CreditGrantReversal, AmountMicro: -5, Reference: "re_4",
		},
		"a paid grant that reverses another": {
			Kind: store.CreditGrantPaid, AmountMicro: 5, Reference: "pi_5", Reverses: "pi_4",
		},
		"an unknown kind": {Kind: "gift", AmountMicro: 5},
	} {
		if _, err := s.RecordCreditGrant(ctx, req); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if n := grantCount(t, s); n != 1 {
		t.Fatalf("grants = %d, want only the paid row", n)
	}
}

// A reversal of a payment the team already spent still holds; the claim path
// is where an empty balance stops new metered work.
func TestCreditReversalMayTakeTheBalanceBelowZero(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "pi_6", "billing"); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, fmt.Sprintf(`INSERT INTO credit_charges
		(team, id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at)
		VALUES ('%s', 'spent', 'run-6', 'n', '', '%s', 1, %d, 1)`,
		store.DefaultTeam, store.CreditChargeUsage, 60*store.MicroCreditsPerCent)); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if _, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -100 * store.MicroCreditsPerCent,
		Reference: "re_6", Reverses: "pi_6", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("reversal: %v", err)
	}
	balance, err := s.CreditBalanceMicro(ctx)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if want := int64(-60 * store.MicroCreditsPerCent); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}
}

// A ledger sums grants in SQL, so an amount near the integer limit would turn
// every later balance read into an overflow error. A paid grant is at most one
// purchase, the balance cap refuses a free grant long before the ceiling, and
// the ceiling still bounds a reversal.
func TestCreditGrantAmountIsBounded(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	purchase := int64(store.CreditPurchaseMaxCents * store.MicroCreditsPerCent)
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		purchase+1, "pi_over", "billing"); err == nil {
		t.Fatal("a paid grant above the largest purchase was accepted")
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree,
		store.MaxCreditGrantMicro, "gift_ceiling", "ops"); !errors.Is(err, store.ErrCreditBalanceCap) {
		t.Fatalf("a free grant at the ceiling = %v, want the balance cap to refuse it", err)
	}
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		purchase, "pi_cap", "billing"); err != nil {
		t.Fatalf("a paid grant of the largest purchase was refused: %v", err)
	}
	for name, req := range map[string]store.CreditGrantRequest{
		"a paid grant over the ceiling": {
			Kind: store.CreditGrantPaid, AmountMicro: store.MaxCreditGrantMicro + 1, Reference: "pi_big",
		},
		"a free grant over the ceiling": {
			Kind: store.CreditGrantFree, AmountMicro: store.MaxCreditGrantMicro + 1,
		},
		"a reversal past the floor": {
			Kind: store.CreditGrantReversal, AmountMicro: math.MinInt64,
			Reference: "re_big", Reverses: "pi_cap",
		},
	} {
		if _, err := s.RecordCreditGrant(ctx, req); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	if _, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -store.MaxCreditGrantMicro,
		Reference: "re_cap", Reverses: "pi_cap", CreatedBy: "billing",
	}); !errors.Is(err, store.ErrReversalExceedsPayment) {
		t.Fatalf("a reversal larger than its payment = %v, want ErrReversalExceedsPayment", err)
	}
	if _, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -purchase,
		Reference: "re_cap", Reverses: "pi_cap", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("a reversal of the whole payment was refused: %v", err)
	}
	state, err := s.CreditState(ctx, 0)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.BalanceMicro != 0 || state.ReversedMicro != purchase {
		t.Fatalf("state = %+v, want the payment reversed", state)
	}
	if _, err := s.CreditLedgerTotals(ctx); err != nil {
		t.Fatalf("totals over the ceiling amounts: %v", err)
	}
}

// A webhook replay carries the terms it carried the first time, so different
// terms under one reference are a mistake rather than a retry.
func TestGrantCreditsRefusesOneReferenceUnderDifferentTerms(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	paid := store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 1000 * store.MicroCreditsPerCent,
		Reference: "pi_terms", CreatedBy: "billing",
	}
	if _, err := s.RecordCreditGrant(ctx, paid); err != nil {
		t.Fatalf("paid grant: %v", err)
	}
	replay, err := s.RecordCreditGrant(ctx, paid)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if replay.Created {
		t.Fatal("an identical replay wrote a second grant")
	}

	other := paid
	other.AmountMicro = 999 * store.MicroCreditsPerCent
	if _, err := s.RecordCreditGrant(ctx, other); !errors.Is(err, store.ErrCreditGrantConflict) {
		t.Fatalf("a different amount under one reference = %v, want a conflict", err)
	}

	if _, err := s.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: 500 * store.MicroCreditsPerCent,
		Reference: "pi_other", CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("second payment: %v", err)
	}
	reversal := store.CreditGrantRequest{
		Kind: store.CreditGrantReversal, AmountMicro: -100 * store.MicroCreditsPerCent,
		Reference: "re_terms", Reverses: "pi_terms", CreatedBy: "billing",
	}
	if _, err := s.RecordCreditGrant(ctx, reversal); err != nil {
		t.Fatalf("reversal: %v", err)
	}
	moved := reversal
	moved.Reverses = "pi_other"
	if _, err := s.RecordCreditGrant(ctx, moved); !errors.Is(err, store.ErrCreditGrantConflict) {
		t.Fatalf("a refund id pointed at another payment = %v, want a conflict", err)
	}
	if n := grantCount(t, s); n != 3 {
		t.Fatalf("grants = %d, want the two payments and the one reversal", n)
	}
}
