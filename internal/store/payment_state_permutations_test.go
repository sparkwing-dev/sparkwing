package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentStateEveryRefundDeliveryPermutation(t *testing.T) {
	for _, refill := range []bool{false, true} {
		for _, outcome := range []string{CardRefundSucceeded, "failed", "canceled"} {
			for _, order := range deliveryOrders("pay", "pending", "terminal", "warning") {
				t.Run(fmt.Sprintf("refill=%t/%s/%s", refill, outcome, strings.Join(order, "-")), func(t *testing.T) {
					s, tenant, now := providerPaymentFixture(t)
					pay := capability.PaymentEvent{EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid, Fingerprint: "fp_1"}
					if refill {
						pay = providerRefillEvent(t, s, now, capability.PaymentCardPaid)
					}
					pending := providerPaymentEvent(now, capability.PaymentRefund)
					pending.EventID, pending.Status, pending.AmountMinor = "refund-pending", CardRefundPending, pay.AmountMinor
					terminal := pending
					terminal.EventID, terminal.Status = "refund-terminal", outcome
					warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
					warning.AmountMinor, warning.Currency, warning.Fingerprint, warning.Actionable = 0, "", "fp_1", true
					events := map[string]capability.PaymentEvent{"pay": pay, "pending": pending, "terminal": terminal, "warning": warning}
					for _, name := range order {
						for range 2 {
							if _, err := s.settlePaymentFacts(t.Context(), events[name]); err != nil {
								t.Fatalf("%s: %v", name, err)
							}
						}
						duplicate := events[name]
						duplicate.EventID += "-distinct-delivery"
						if _, err := s.settlePaymentFacts(t.Context(), duplicate); err != nil {
							t.Fatalf("distinct %s: %v", name, err)
						}
					}
					pending.EventID = "stale-pending-after-terminal"
					if _, err := s.settlePaymentFacts(t.Context(), pending); err != nil {
						t.Fatal(err)
					}
					for _, name := range order {
						if _, err := s.settlePaymentFacts(t.Context(), events[name]); err != nil {
							t.Fatalf("delayed duplicate %s: %v", name, err)
						}
					}
					if outcome != CardRefundSucceeded {
						for range 2 {
							if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment-0", pending.ObjectID); err != nil {
								t.Fatal(err)
							}
						}
					}
					var status string
					if err := s.db.QueryRow(`SELECT status FROM payment_refunds WHERE payment_mode='test' AND kind='refund' AND refund_id=?`, pending.ObjectID).Scan(&status); err != nil || status != outcome {
						t.Fatalf("refund status=%q, want terminal %q: %v", status, outcome, err)
					}
					assertPaymentStateFinancialResult(t, s, tenant, now, outcome != CardRefundSucceeded)
				})
			}
		}
	}
}

func TestPaymentStateEveryRefundResponseDeliveryPermutation(t *testing.T) {
	for _, outcome := range []string{CardRefundSucceeded, "failed", "canceled"} {
		for _, order := range deliveryOrders("response", "pending", "terminal") {
			t.Run(outcome+"/"+strings.Join(order, "-"), func(t *testing.T) {
				s, tenant, now := queuedRefundFixture(t, true)
				pending := providerPaymentEvent(now, capability.PaymentRefund)
				pending.EventID, pending.ObjectID, pending.Status, pending.AmountMinor = "pending", "refund", CardRefundPending, 1_000
				terminal := pending
				terminal.EventID, terminal.Status = "terminal", outcome
				for _, name := range order {
					for range 2 {
						if name == "response" {
							if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment-0", "refund"); err != nil {
								t.Fatal(err)
							}
						} else {
							event := pending
							if name == "terminal" {
								event = terminal
							}
							if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
								t.Fatalf("%s: %v", name, err)
							}
						}
					}
				}
				pending.EventID = "stale-pending"
				if _, err := s.settlePaymentFacts(t.Context(), pending); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := s.db.QueryRow(`SELECT status FROM payment_refunds WHERE payment_mode='test' AND kind='refund' AND refund_id='refund'`).Scan(&status); err != nil || status != outcome {
					t.Fatalf("response did not share terminal state: %q, want %q: %v", status, outcome, err)
				}
				assertPaymentStateFinancialResult(t, s, tenant, now, outcome != CardRefundSucceeded)
			})
		}
	}
}

func TestPaymentStateEveryDisputeDeliveryPermutation(t *testing.T) {
	for _, outcome := range []string{"lost", "won"} {
		for _, order := range deliveryOrders("pay", "open", "closed") {
			t.Run(outcome+"/"+strings.Join(order, "-"), func(t *testing.T) {
				s, tenant, now := providerPaymentFixture(t)
				pay := providerRefillEvent(t, s, now, capability.PaymentCardPaid)
				opened := providerPaymentEvent(now, capability.PaymentDisputeOpened)
				opened.AmountMinor, opened.Status = pay.AmountMinor, "needs_response"
				closed := opened
				closed.Kind, closed.EventID, closed.Status = capability.PaymentDisputeClosed, "dispute-closed", outcome
				events := map[string]capability.PaymentEvent{"pay": pay, "open": opened, "closed": closed}
				for _, name := range order {
					for range 2 {
						if _, err := s.settlePaymentFacts(t.Context(), events[name]); err != nil {
							t.Fatalf("%s: %v", name, err)
						}
					}
				}
				opened.EventID = "stale-dispute-open"
				if _, err := s.settlePaymentFacts(t.Context(), opened); err != nil {
					t.Fatal(err)
				}
				wantBalance := pay.AmountMinor * MicroCreditsPerCent
				if outcome == "lost" {
					wantBalance = 0
				}
				if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != wantBalance {
					t.Fatalf("balance=%d, want %d: %v", balance, wantBalance, err)
				}
				if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || freeze.Frozen != (outcome == "lost") {
					t.Fatalf("dispute hold=%+v: %v", freeze, err)
				}
			})
		}
	}
}

func TestPaymentStateEverySetupDeliveryPermutation(t *testing.T) {
	for _, order := range deliveryOrders("page", "setup") {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			rewriteSchemaExec(t, s.db, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test','setup-page','setup_session','a','cus_1')`)
			page := capability.PaymentEvent{EventID: "page-completed", ObjectID: "setup-page", IntentID: "setup-intent", CustomerID: "cus_1", Mode: capability.PaymentTest, Kind: capability.PaymentPageCompleted, Created: now.Unix() + 1}
			setup := capability.PaymentEvent{EventID: "setup-completed", ObjectID: "setup-intent", CustomerID: "cus_1", MethodID: "new-method", Fingerprint: "new-fingerprint", Brand: "visa", Last4: "4242", Status: "succeeded", Mode: capability.PaymentTest, Kind: capability.PaymentCardSetup, Created: now.Unix() + 2}
			events := map[string]capability.PaymentEvent{"page": page, "setup": setup}
			for _, name := range order {
				for range 2 {
					if _, err := s.settlePaymentFacts(t.Context(), events[name]); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
				}
			}
			standing, err := tenant.SpendStanding(t.Context(), now)
			if err != nil || standing.Card.PaymentMethod != "new-method" {
				t.Fatalf("setup was lost before page association: %+v: %v", standing.Card, err)
			}
		})
	}
}

func TestPaymentStateEveryRefillAssociationPermutation(t *testing.T) {
	for _, order := range deliveryOrders("bind", "success", "failure") {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return setRefillTx(t.Context(), tx, "a", RefillSettings{Enabled: true, ThresholdCents: 10_000_000, AmountCents: 15_000}, "owner")
			}); err != nil {
				t.Fatal(err)
			}
			due, err := s.AsOperator().DueRefills(t.Context())
			if err != nil || len(due) != 1 {
				t.Fatalf("reservation=%+v: %v", due, err)
			}
			paid := refillPaid(now, "payment", 15_000)
			failed := paid
			failed.Kind, failed.EventID, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "failed", "requires_payment_method", "card_declined"
			failed.Fingerprint, failed.ChargeID = "", ""
			for _, name := range order {
				for range 2 {
					if name == "bind" {
						if err := s.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "payment"); err != nil {
							t.Fatal(err)
						}
					} else {
						event := paid
						if name == "failure" {
							event = failed
						}
						if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
							t.Fatalf("%s before or after association: %v", name, err)
						}
					}
				}
			}
			if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 15_000*MicroCreditsPerCent {
				t.Fatalf("successful payment lost across binding: %d: %v", balance, err)
			}
			var failure string
			if err := s.db.QueryRow(`SELECT failure FROM credit_checkouts WHERE kind='refill' AND payment_id='payment'`).Scan(&failure); err != nil || failure != "" {
				t.Fatalf("terminal success regressed to failure: %q: %v", failure, err)
			}
		})
	}
}

func TestPaymentStateProviderModesKeepIndependentConsequences(t *testing.T) {
	for _, kind := range []capability.PaymentEventKind{capability.PaymentRefund, capability.PaymentDisputeClosed, capability.PaymentWarningEvent} {
		t.Run(string(kind), func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			paid := capability.PaymentEvent{EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
			consequence := providerPaymentEvent(now, kind)
			consequence.ObjectID, consequence.Status = "same-provider-object", CardRefundSucceeded
			if kind == capability.PaymentDisputeClosed {
				consequence.Status = "lost"
			}
			if kind == capability.PaymentWarningEvent {
				consequence.AmountMinor, consequence.Currency, consequence.Actionable = 0, "", true
			}
			for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
				if mode == capability.PaymentLive {
					if err := s.AsOperator().SetPaymentMode(t.Context(), mode); err != nil {
						t.Fatal(err)
					}
					rewriteSchemaExec(t, s.db, `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('live','a','live-session','live','usd',100,100000000,1,2)`)
					paid.CheckoutID = "live-session"
				}
				paid.Mode, consequence.Mode = mode, mode
				if _, err := s.settlePaymentFacts(t.Context(), paid); err != nil {
					t.Fatal(err)
				}
				if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 100*MicroCreditsPerCent {
					t.Fatalf("%s payment consumed another mode's consequence: %d: %v", mode, balance, err)
				}
				if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || freeze.Frozen {
					t.Fatalf("%s payment revived another mode's hold: %+v: %v", mode, freeze, err)
				}
				if _, err := s.settlePaymentFacts(t.Context(), consequence); err != nil {
					t.Fatal(err)
				}
				if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
					t.Fatalf("%s consequence did not reverse its own payment: %d: %v", mode, balance, err)
				}
				if kind != capability.PaymentRefund {
					freeze, err := tenant.CreditFreeze(t.Context())
					if err != nil || !freeze.Frozen || len(freeze.Disputes) != 1 {
						t.Fatalf("%s consequence reused a released hold: %+v: %v", mode, freeze, err)
					}
					if _, err := tenant.ReleaseCreditFreezes(t.Context(), freeze.Disputes[0], "operator", "reviewed", now); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func assertPaymentStateFinancialResult(t *testing.T, s *Store, tenant *Tenant, now time.Time, refundDue bool) {
	t.Helper()
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("balance=%d, want zero after warning/refund: %v", balance, err)
	}
	if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || !freeze.Frozen {
		t.Fatalf("warning hold=%+v: %v", freeze, err)
	}
	due, err := s.AsOperator().DueCardRefunds(t.Context(), now.Add(24*time.Hour))
	want := 0
	if refundDue {
		want = 1
	}
	if err != nil || len(due) != want {
		t.Fatalf("refunds=%+v, want %d: %v", due, want, err)
	}
	if refundDue && due[0].Key != "payment-1" {
		t.Fatalf("refund terminal applied more or less than once: %+v", due[0])
	}
}
