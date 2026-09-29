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

func dollarsMicro(n int64) int64 { return n * 100 * store.MicroCreditsPerCent }

func payTeam(t *testing.T, tenant *store.Tenant, ref string, dollars int64) time.Time {
	t.Helper()
	res, err := tenant.RecordCreditGrant(context.Background(), store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollarsMicro(dollars), Reference: ref, CreatedBy: "billing",
	})
	if err != nil {
		t.Fatalf("pay %s: %v", ref, err)
	}
	return res.Grant.CreatedAt
}

// spend seeds a usage charge and its day's spend bucket, as the ledger's own
// charge insert does.
func spend(t *testing.T, s *store.Store, team, id string, cents int64) {
	t.Helper()
	now := time.Now().UnixNano()
	amount := cents * store.MicroCreditsPerCent
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO credit_charges (id, run_id, node_id, token_prefix, kind, seconds, amount_micro, charged_at, team)
		 VALUES ('%s', 'run-x', 'build', 'pfx', 'usage', 60, %d, %d, '%s')`,
		id, amount, now, team)); err != nil {
		t.Fatalf("seed a charge: %v", err)
	}
	if _, err := s.DB().Exec(fmt.Sprintf(
		`INSERT INTO team_spend_days (team, day, amount_micro) VALUES ('%s', %d, %d)
		 ON CONFLICT (team, day) DO UPDATE SET amount_micro = team_spend_days.amount_micro + excluded.amount_micro`,
		team, now/int64(24*time.Hour), amount)); err != nil {
		t.Fatalf("seed a spend day: %v", err)
	}
}

func billingStanding(t *testing.T, tenant *store.Tenant, now time.Time) store.BillingStanding {
	t.Helper()
	b, err := tenant.BillingStanding(context.Background(), now)
	if err != nil {
		t.Fatalf("standing: %v", err)
	}
	return b
}

// A new team buys at most $50 over 30 days, counting payments and open
// checkouts, and a refusal names the figures.
func TestNewTeamPurchaseLimit(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	hold := 31 * time.Minute

	payTeam(t, acme, "pi_1", 30)
	if _, err := acme.OpenCreditCheckout(ctx, dollarsMicro(10), now, hold); err != nil {
		t.Fatalf("a checkout inside the limit: %v", err)
	}
	_, err := acme.OpenCreditCheckout(ctx, dollarsMicro(10)+store.MicroCreditsPerCent, now, hold)
	var limitErr *store.PurchaseLimitError
	if !errors.As(err, &limitErr) || !errors.Is(err, store.ErrPurchaseLimit) {
		t.Fatalf("a checkout one cent past the limit = %v, want a PurchaseLimitError", err)
	}
	if limitErr.Trusted || limitErr.LimitMicro != dollarsMicro(50) || limitErr.PurchasedMicro != dollarsMicro(40) {
		t.Fatalf("refusal figures = %+v", limitErr)
	}
	if _, err := acme.OpenCreditCheckout(ctx, dollarsMicro(10), now, hold); err != nil {
		t.Fatalf("a checkout filling the limit exactly: %v", err)
	}
	if b := billingStanding(t, acme, now); b.PurchasedMicro != dollarsMicro(50) || b.PurchaseMaxCents() != store.NewPurchaseMaxCents {
		t.Fatalf("standing = %+v", b)
	}

	later := now.Add(store.PurchaseLimitWindow + time.Hour)
	if _, err := acme.OpenCreditCheckout(ctx, dollarsMicro(50), later, hold); err != nil {
		t.Fatalf("a checkout once the window passed: %v", err)
	}
}

// A team earns trust only when every part of the rule holds: an unreversed
// payment at least 30 days old, $50 spent, and no dispute hold, ever.
func TestAutomaticTrustRuleBoundaries(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	acme := teamHandle(t, s, "acme")
	paidAt := payTeam(t, acme, "pi_acme", 50)
	spend(t, s, "acme", "ch_acme", 5_000)
	aged := paidAt.Add(store.AutoTrustPaymentAge)
	if b := billingStanding(t, acme, aged.Add(-time.Nanosecond)); b.Trusted {
		t.Fatal("trusted a nanosecond before the payment was 30 days old")
	}
	b := billingStanding(t, acme, aged)
	if !b.Trusted || b.LimitMicro != dollarsMicro(500) || b.PurchaseMaxCents() != store.CreditPurchaseMaxCents {
		t.Fatalf("at 30 days with $50 spent, standing = %+v, want trusted at $500", b)
	}

	short := teamHandle(t, s, "short")
	paidAt = payTeam(t, short, "pi_short", 50)
	spend(t, s, "short", "ch_short", 4_999)
	if billingStanding(t, short, paidAt.Add(store.AutoTrustPaymentAge)).Trusted {
		t.Fatal("trusted with a cent under $50 spent")
	}

	held := teamHandle(t, s, "held")
	paidAt = payTeam(t, held, "pi_held", 50)
	spend(t, s, "held", "ch_held", 5_000)
	if _, err := s.HoldTeamForDispute(ctx, "held", "dp_1", "pi_held", "dispute", paidAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseCreditFreezes(ctx, "held", "dp_1", paidAt); err != nil {
		t.Fatal(err)
	}
	if billingStanding(t, held, paidAt.Add(store.AutoTrustPaymentAge)).Trusted {
		t.Fatal("trusted a team once held over a dispute, released or not")
	}

	reversed := teamHandle(t, s, "reversed")
	paidAt = payTeam(t, reversed, "pi_rev", 50)
	spend(t, s, "reversed", "ch_rev", 5_000)
	if _, err := s.ReversePayment(ctx, "pi_rev", "re_1", "ops", 0); err != nil {
		t.Fatal(err)
	}
	if billingStanding(t, reversed, paidAt.Add(store.AutoTrustPaymentAge)).Trusted {
		t.Fatal("trusted on the age of a reversed payment")
	}

	unpaid := teamHandle(t, s, "unpaid")
	spend(t, s, "unpaid", "ch_unpaid", 10_000)
	if billingStanding(t, unpaid, time.Now().Add(365*24*time.Hour)).Trusted {
		t.Fatal("trusted a team that never paid")
	}
}

// An operator's grant trusts a team at once, and a revocation holds it to the
// new-team limits even when the automatic rule passes.
func TestOperatorTrustGrantAndRevoke(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()

	before, after, err := acme.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustGranted, Actor: "korey", Reason: "known customer",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if before.Trusted || !after.Trusted || after.TrustBy != "korey" || after.TrustReason != "known customer" ||
		after.LimitMicro != dollarsMicro(500) {
		t.Fatalf("grant: before %+v, after %+v", before, after)
	}
	if _, err := acme.OpenCreditCheckout(ctx, dollarsMicro(500), now, time.Hour); err != nil {
		t.Fatalf("a $500 checkout on a granted team: %v", err)
	}

	_, after, err = acme.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustGranted, Actor: "korey", Reason: "reviewed", LimitCents: 200_000,
	}, now)
	if err != nil || after.LimitMicro != dollarsMicro(2_000) {
		t.Fatalf("an override = %+v, %v; want a $2,000 limit", after, err)
	}

	earned := teamHandle(t, s, "earned")
	paidAt := payTeam(t, earned, "pi_earned", 50)
	spend(t, s, "earned", "ch_earned", 5_000)
	aged := paidAt.Add(store.AutoTrustPaymentAge)
	if !billingStanding(t, earned, aged).Trusted {
		t.Fatal("the rule should trust this team before the revocation")
	}
	if _, _, err := earned.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustRevoked, Actor: "korey", Reason: "chargeback elsewhere",
	}, aged); err != nil {
		t.Fatal(err)
	}
	b := billingStanding(t, earned, aged)
	if b.Trusted || b.LimitMicro != dollarsMicro(50) {
		t.Fatalf("revoked standing = %+v, want new-team limits", b)
	}
	if _, err := earned.OpenCreditCheckout(ctx, dollarsMicro(20), aged, time.Hour); !errors.Is(err, store.ErrPurchaseLimit) {
		t.Fatalf("a revoked team's $20 checkout = %v, want the new-team limit", err)
	}
	_, after, err = earned.SetBillingTrust(ctx, store.BillingTrustChange{
		Trust: store.BillingTrustAutomatic, Actor: "korey", Reason: "resolved",
	}, aged)
	if err != nil || after.Trust != store.BillingTrustAutomatic || !after.Trusted || after.TrustReason != "resolved" {
		t.Fatalf("reset = %+v, %v; want the automatic rule to trust the team again", after, err)
	}

	for _, bad := range []store.BillingTrustChange{
		{Trust: "trusted", Actor: "korey", Reason: "x"},
		{Trust: store.BillingTrustAutomatic, Actor: "korey", Reason: "x", LimitCents: 100_000},
		{Trust: store.BillingTrustGranted, Actor: "korey"},
		{Trust: store.BillingTrustRevoked, Actor: "korey", Reason: "x", LimitCents: 100_000},
		{Trust: store.BillingTrustGranted, Actor: "korey", Reason: "x", LimitCents: store.MaxPurchaseLimitCents + 1},
	} {
		if _, _, err := acme.SetBillingTrust(ctx, bad, now); !errors.Is(err, store.ErrInvalidInput) {
			t.Errorf("SetBillingTrust(%+v) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

// A session paid just before it expired is granted when its webhook arrives,
// which can be days later, so its amount keeps counting against the limit
// after expiry: a second checkout cannot spend the same room.
func TestAnExpiredSessionKeepsCountingUntilItsPaymentCanNoLongerArrive(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	hold := 31 * time.Minute

	id, err := acme.OpenCreditCheckout(ctx, dollarsMicro(50), now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := acme.AttachCreditCheckout(ctx, id, "cs_late", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	afterExpiry := now.Add(hold + time.Hour)
	if _, err := acme.OpenCreditCheckout(ctx, dollarsMicro(10), afterExpiry, hold); !errors.Is(err, store.ErrPurchaseLimit) {
		t.Fatalf("a checkout beside an expired, unsettled $50 session = %v, want the purchase limit", err)
	}
	if _, err := acme.RecordCreditGrant(ctx, store.CreditGrantRequest{
		Kind: store.CreditGrantPaid, AmountMicro: dollarsMicro(50), Reference: "pi_late", Checkout: "cs_late",
		CreatedBy: "billing",
	}); err != nil {
		t.Fatalf("the late paid grant: %v", err)
	}
	if b := billingStanding(t, acme, afterExpiry); b.PurchasedMicro != dollarsMicro(50) {
		t.Fatalf("after the late grant, purchased = %d, want the payment counted once", b.PurchasedMicro)
	}

	globex := teamHandle(t, s, "globex")
	id, err = globex.OpenCreditCheckout(ctx, dollarsMicro(50), now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := globex.AttachCreditCheckout(ctx, id, "cs_abandoned", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	settled := now.Add(hold + store.CheckoutSettleWindow + time.Second)
	if _, err := globex.OpenCreditCheckout(ctx, dollarsMicro(50), settled, hold); err != nil {
		t.Fatalf("a checkout once the abandoned session could no longer be paid: %v", err)
	}
}
