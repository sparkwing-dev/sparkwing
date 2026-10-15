package store

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func checkoutCapFixture(t *testing.T, trusted bool) (*Store, *Tenant, *Tenant, *time.Time) {
	t.Helper()
	s := authorityStore(t)
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	a, b := &Tenant{s: s, team: "a"}, &Tenant{s: s, team: "b"}
	if trusted {
		for _, tenant := range []*Tenant{a, b} {
			if _, _, err := tenant.SetBillingTrust(t.Context(), BillingTrustChange{Trust: BillingTrustGranted, Actor: "ops", Reason: "reviewed", LimitCents: MaxPurchaseLimitCents}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, a, b, &now
}

func TestPaymentCheckoutCapCountsVerifiedSettlementOnce(t *testing.T) {
	s, a, b, now := checkoutCapFixture(t, true)
	const usd = 100 * MicroCreditsPerCent
	hold := 31 * time.Minute
	for _, tenant := range []*Tenant{a, b} {
		if _, err := tenant.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantFree, AmountMicro: 4000 * usd, Reference: "gift", CreatedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := a.OpenCreditCheckout(t.Context(), 600*usd, *now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AttachCreditCheckout(t.Context(), first, "first", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	_, err = a.OpenCreditCheckout(t.Context(), 600*usd, *now, hold)
	var capErr *CreditBalanceCapError
	if !errors.As(err, &capErr) || capErr.BalanceMicro != 4000*usd || capErr.OpenMicro != 600*usd {
		t.Fatalf("open checkout did not hold balance room: %v", err)
	}
	if _, err := a.OpenCreditCheckout(t.Context(), 400*usd, *now, hold); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(hold + time.Second)
	if _, err := a.OpenCreditCheckout(t.Context(), 1000*usd, *now, hold); err != nil {
		t.Fatalf("expired balance holds remained: %v", err)
	}
	id, err := b.OpenCreditCheckout(t.Context(), 500*usd, *now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AttachCreditCheckout(t.Context(), id, "paid-session", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	event := capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "paid", CheckoutID: "paid-session", PaymentID: "payment", Kind: capability.PaymentCheckoutPaid, AmountMinor: 50000, Currency: "usd"}
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.OpenCreditCheckout(t.Context(), 500*usd, *now, hold); err != nil {
		t.Fatalf("paid checkout still held balance room: %v", err)
	}
	if _, err := b.OpenCreditCheckout(t.Context(), 5*usd, *now, hold); !errors.Is(err, ErrCreditBalanceCap) {
		t.Fatalf("checkout beyond balance cap=%v", err)
	}
}

func TestPaymentCheckoutLateSettlementCountsPurchaseOnce(t *testing.T) {
	s, a, b, now := checkoutCapFixture(t, false)
	const usd = 100 * MicroCreditsPerCent
	hold := 31 * time.Minute
	id, err := a.OpenCreditCheckout(t.Context(), 50*usd, *now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AttachCreditCheckout(t.Context(), id, "late-session", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(hold + time.Hour)
	if _, err := a.OpenCreditCheckout(t.Context(), 10*usd, *now, hold); !errors.Is(err, ErrPurchaseLimit) {
		t.Fatalf("unsettled expired purchase lost its hold: %v", err)
	}
	event := capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "paid", CheckoutID: "late-session", PaymentID: "payment", Kind: capability.PaymentCheckoutPaid, AmountMinor: 5000, Currency: "usd"}
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	standing, err := a.BillingStanding(t.Context(), *now)
	if err != nil || standing.PurchasedMicro != 50*usd {
		t.Fatalf("late payment counted more than once: %+v: %v", standing, err)
	}
	id, err = b.OpenCreditCheckout(t.Context(), 50*usd, *now, hold)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AttachCreditCheckout(t.Context(), id, "abandoned-session", now.Add(hold)); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(hold + CheckoutSettleWindow + time.Second)
	if _, err := b.OpenCreditCheckout(t.Context(), 50*usd, *now, hold); err != nil {
		t.Fatalf("abandoned purchase hold outlived settlement window: %v", err)
	}
}

func TestPaymentCheckoutRawGrantCannotSettleProviderSession(t *testing.T) {
	s, a, _, now := checkoutCapFixture(t, false)
	id, err := a.OpenCreditCheckout(t.Context(), MicroCreditsPerCent, *now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AttachCreditCheckout(t.Context(), id, "provider-session", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecordCreditGrant(t.Context(), CreditGrantRequest{Kind: CreditGrantPaid, AmountMicro: MicroCreditsPerCent, Reference: "provider-session", CreatedBy: "billing"}); err != nil {
		t.Fatal(err)
	}
	var unpaid int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM credit_checkouts WHERE team='a' AND id=? AND paid_at IS NULL AND grant_id IS NULL", id).Scan(&unpaid); err != nil || unpaid != 1 {
		t.Fatalf("raw grant settled checkout: unpaid=%d: %v", unpaid, err)
	}
}
