package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func paymentFixture(t *testing.T, mode capability.PaymentMode) (*Store, capability.PaymentEvent) {
	t.Helper()
	s := authorityStore(t)
	rewriteSchemaExec(t, s.db, "INSERT INTO settings(key,value) VALUES ('payment_mode',?)", []byte(`"`+string(mode)+`"`))
	rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('checkout','a','provider-session',?,'usd',100,1000000,1,2)`, string(mode))
	return s, capability.PaymentEvent{EventID: "event", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: mode, Kind: capability.PaymentCheckoutPaid}
}

func TestPaymentSettlementGrantsOnceAcrossEventsAndModes(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			s, event := paymentFixture(t, mode)
			first, err := s.settlePaymentFacts(t.Context(), event)
			if err != nil {
				t.Fatal(err)
			}
			if first.Team != "a" || first.AmountMicro != 1000000 || first.GrantID == "" || first.Replayed {
				t.Fatalf("first settlement: %#v", first)
			}
			for _, id := range []string{"event", "duplicate-paid", "late-failure", "late-expiry"} {
				event.EventID = id
				if id == "late-failure" {
					event.Kind = capability.PaymentCheckoutFailed
				}
				if id == "late-expiry" {
					event.Kind = capability.PaymentCheckoutExpired
				}
				got, err := s.settlePaymentFacts(t.Context(), event)
				if err != nil {
					t.Fatal(err)
				}
				if got.GrantID != first.GrantID || !got.Replayed {
					t.Fatalf("duplicate settlement: %#v", got)
				}
			}
			rewriteSchemaCount(t, s.db, "credit_grants", 1)
			rewriteSchemaCount(t, s.db, "payment_events", 4)
		})
	}
}

func TestPaymentSettlementRefusesUnverifiedOrConflictingFacts(t *testing.T) {
	for _, kind := range []string{"zero", "mode", "amount", "currency", "checkout", "payment", "event_digest", "missing_mode"} {
		t.Run(kind, func(t *testing.T) {
			s, event := paymentFixture(t, capability.PaymentTest)
			if kind == "zero" {
				if _, err := s.SettlePayment(t.Context(), capability.VerifiedPaymentEvent{}); err == nil {
					t.Fatal("zero proof accepted")
				}
				return
			}
			wantGrants := 0
			switch kind {
			case "mode":
				event.Mode = capability.PaymentLive
			case "amount":
				event.AmountMinor++
			case "currency":
				event.Currency = "eur"
			case "checkout":
				event.CheckoutID = "unknown"
			case "missing_mode":
				rewriteSchemaExec(t, s.db, "DELETE FROM settings WHERE key = 'payment_mode'")
			case "payment":
				rewriteSchemaExec(t, s.db, "UPDATE credit_checkouts SET payment_id = 'another' WHERE id = 'checkout'")
			case "event_digest":
				if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				wantGrants = 1
				event.Kind = capability.PaymentCheckoutFailed
			}
			if _, err := s.settlePaymentFacts(t.Context(), event); err == nil {
				t.Fatal("conflicting facts accepted")
			}
			rewriteSchemaCount(t, s.db, "credit_grants", wantGrants)
		})
	}
}

func TestPaymentSettlementRollbackAndLatePayment(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	event.Kind = capability.PaymentCheckoutExpired
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 0)
	event.EventID = "paid"
	event.Kind = capability.PaymentCheckoutPaid
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER refuse_payment_event BEFORE INSERT ON payment_events BEGIN SELECT RAISE(ABORT,'event storage unavailable'); END`)
	if _, err := s.settlePaymentFacts(t.Context(), event); err == nil {
		t.Fatal("accepted payment without durable event")
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 0)
	rewriteSchemaExec(t, s.db, "DROP TRIGGER refuse_payment_event")
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 1)
}

func TestPaymentSettlementAuditsTransitionsAndResumesFundedTeam(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	rewriteSchemaExec(t, s.db, "UPDATE teams SET credit_exhausted_at=1 WHERE name='a'")
	event.Kind = capability.PaymentCheckoutExpired
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.EventID, event.Kind = "failed", capability.PaymentCheckoutFailed
	if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	event.EventID, event.Kind = "paid", capability.PaymentCheckoutPaid
	for range 2 {
		if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	var exhausted int64
	if err := s.db.QueryRow("SELECT credit_exhausted_at FROM teams WHERE name='a'").Scan(&exhausted); err != nil || exhausted != 0 {
		t.Fatalf("paid team remains exhausted: %d, %v", exhausted, err)
	}
	for kind, want := range map[string]int{BusinessEventCheckoutExpired: 1, BusinessEventCheckoutFailed: 0, BusinessEventCheckoutPaid: 1} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM business_events WHERE team='a' AND kind=?", kind).Scan(&count); err != nil || count != want {
			t.Fatalf("%s events=%d, want %d: %v", kind, count, want, err)
		}
	}
}

func TestPaymentSettlementAuditFailureRollsBackFinancialResult(t *testing.T) {
	s, event := paymentFixture(t, capability.PaymentTest)
	rewriteSchemaExec(t, s.db, "CREATE TRIGGER refuse_payment_audit BEFORE INSERT ON business_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
	if _, err := s.settlePaymentFacts(t.Context(), event); err == nil {
		t.Fatal("financial write ignored audit failure")
	}
	rewriteSchemaCount(t, s.db, "credit_grants", 0)
	rewriteSchemaCount(t, s.db, "payment_events", 0)
	var paid int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM credit_checkouts WHERE paid_at IS NOT NULL").Scan(&paid); err != nil || paid != 0 {
		t.Fatalf("checkout advanced without audit: %d, %v", paid, err)
	}
}
