package store

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentCheckoutCreatesSettlementFacts(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			s := authorityStore(t)
			tenant := &Tenant{s: s, team: "a"}
			now := time.Unix(1000, 0).UTC()
			s.clock = func() time.Time { return now }
			rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"`+string(mode)+`"`))
			id, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.TestOnlyAttachCreditCheckout(t.Context(), id, "provider-session", now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			event := capability.PaymentEvent{Mode: mode, EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Kind: capability.PaymentCheckoutPaid, AmountMinor: 100, Currency: "usd"}
			result, err := s.settlePaymentFacts(t.Context(), event)
			if err != nil || result.CheckoutID != id || result.AmountMicro != 100*MicroCreditsPerCent {
				t.Fatalf("settlement=%+v: %v", result, err)
			}
			rewriteSchemaCount(t, s.db, "credit_grants", 1)
		})
	}
}

func TestPaymentCheckoutAttachmentCannotMoveFinancialIdentity(t *testing.T) {
	s, tenant, a := billingPageFixture(t)
	page, err := tenant.PrepareAuthorizedBillingPage(t.Context(), a, capability.BillingPageCredits, 100, "binding")
	if err != nil {
		t.Fatal(err)
	}
	page.ProviderID, page.URL = "provider-session", "https://checkout.test/page"
	if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, page); err != nil {
		t.Fatal(err)
	}
	event := capability.PaymentEvent{Mode: capability.PaymentTest, EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Kind: capability.PaymentCheckoutPaid, AmountMinor: 100, Currency: "usd"}
	for _, paid := range []bool{false, true} {
		if paid {
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		replacement := page
		replacement.ProviderID = "substituted"
		if err := tenant.CompleteAuthorizedBillingPage(t.Context(), a, replacement); err == nil {
			t.Fatal("provider session was replaced")
		}
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
	rewriteSchemaCount(t, s.db, "payment_events", 1)
}

func TestPaymentCheckoutRefusesMissingModeAndInvalidAmount(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1000, 0).UTC()
	if _, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour); err == nil {
		t.Fatal("unconfigured payment accepted")
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	for _, amount := range []int64{0, -1, MicroCreditsPerCent + 1} {
		if _, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), amount, now, time.Hour); err == nil {
			t.Fatalf("accepted amount %d", amount)
		}
	}
	if _, err := tenant.TestOnlyOpenCreditCheckout(t.Context(), MicroCreditsPerCent, now, 0); err == nil {
		t.Fatal("nonpositive hold accepted")
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 0)
}
