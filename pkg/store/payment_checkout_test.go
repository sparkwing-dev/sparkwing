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
			id, err := tenant.OpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.AttachCreditCheckout(t.Context(), id, "provider-session", now.Add(time.Hour)); err != nil {
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
	s, event := paymentFixture(t, capability.PaymentTest)
	tenant := &Tenant{s: s, team: "a"}
	for _, paid := range []bool{false, true} {
		if paid {
			if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		if err := tenant.AttachCreditCheckout(t.Context(), "checkout", "substituted", time.Unix(3, 0)); err == nil {
			t.Fatal("provider session was replaced")
		}
		if err := tenant.DropCreditCheckout(t.Context(), "checkout"); err == nil {
			t.Fatal("attached checkout was deleted")
		}
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
	rewriteSchemaCount(t, s.db, "payment_events", 1)
}

func TestPaymentCheckoutUnattachedAbortIsDurableAndIdempotent(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1000, 0).UTC()
	s.clock = func() time.Time { return now }
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	id, err := tenant.OpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tenant.DropCreditCheckout(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tenant.AttachCreditCheckout(t.Context(), id, "provider-session", now.Add(time.Hour)); err == nil {
		t.Fatal("aborted checkout attached")
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 1)
	var open int64
	if err := s.inTx(t.Context(), func(tx *storeTx) error {
		var err error
		open, err = openCheckoutMicroTx(t.Context(), tx, "a", now.UnixNano())
		return err
	}); err != nil || open != 0 {
		t.Fatalf("aborted checkout reserves %d: %v", open, err)
	}
}

func TestPaymentCheckoutRefusesMissingModeAndInvalidAmount(t *testing.T) {
	s := authorityStore(t)
	tenant := &Tenant{s: s, team: "a"}
	now := time.Unix(1000, 0).UTC()
	if _, err := tenant.OpenCreditCheckout(t.Context(), 100*MicroCreditsPerCent, now, time.Hour); err == nil {
		t.Fatal("unconfigured payment accepted")
	}
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"test"`))
	for _, amount := range []int64{0, -1, MicroCreditsPerCent + 1} {
		if _, err := tenant.OpenCreditCheckout(t.Context(), amount, now, time.Hour); err == nil {
			t.Fatalf("accepted amount %d", amount)
		}
	}
	if _, err := tenant.OpenCreditCheckout(t.Context(), MicroCreditsPerCent, now, 0); err == nil {
		t.Fatal("nonpositive hold accepted")
	}
	rewriteSchemaCount(t, s.db, "credit_checkouts", 0)
}
