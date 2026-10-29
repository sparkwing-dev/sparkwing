package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestPaymentStateEveryRefundDeliveryPermutation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 6.9s measured in the short class; the full suite runs it")
	}
	for _, refill := range []bool{false, true} {
		for _, outcome := range []string{cardRefundSucceeded, "failed", "canceled"} {
			for _, order := range deliveryOrders("pay", "pending", "terminal", "warning") {
				t.Run(fmt.Sprintf("refill=%t/%s/%s", refill, outcome, strings.Join(order, "-")), func(t *testing.T) {
					s, tenant, now := providerPaymentFixture(t)
					pay := capability.PaymentEvent{EventID: "paid", CheckoutID: "provider-session", PaymentID: "payment", Currency: "usd", AmountMinor: 100, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
					if refill {
						pay = providerRefillEvent(t, s, now, capability.PaymentCardPaid)
					}
					pending := providerPaymentEvent(now, capability.PaymentRefund)
					pending.EventID, pending.Status, pending.AmountMinor = "refund-pending", cardRefundPending, pay.AmountMinor
					terminal := pending
					terminal.EventID, terminal.Status = "refund-terminal", outcome
					warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
					warning.AmountMinor, warning.Currency, warning.Actionable = 0, "", true
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
					if outcome != cardRefundSucceeded {
						for range 2 {
							if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment", pending.ObjectID); err != nil {
								t.Fatal(err)
							}
						}
					}
					var status string
					if err := s.db.QueryRow(`SELECT status FROM payment_refunds WHERE payment_mode='test' AND kind='refund' AND refund_id=?`, pending.ObjectID).Scan(&status); err != nil || status != outcome {
						t.Fatalf("refund status=%q, want terminal %q: %v", status, outcome, err)
					}
					assertPaymentStateFinancialResult(t, s, tenant, now, outcome != cardRefundSucceeded)
				})
			}
		}
	}
}

func TestPaymentStateEveryRefundResponseDeliveryPermutation(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.8s measured in the short class; the full suite runs it")
	}
	for _, outcome := range []string{cardRefundSucceeded, "failed", "canceled"} {
		for _, order := range deliveryOrders("response", "pending", "terminal") {
			t.Run(outcome+"/"+strings.Join(order, "-"), func(t *testing.T) {
				s, tenant, now := queuedRefundFixture(t, true)
				pending := providerPaymentEvent(now, capability.PaymentRefund)
				pending.EventID, pending.ObjectID, pending.Status, pending.AmountMinor = "pending", "refund", cardRefundPending, 1_000
				terminal := pending
				terminal.EventID, terminal.Status = "terminal", outcome
				for _, name := range order {
					for range 2 {
						if name == "response" {
							if err := s.AsOperator().MarkCardRefundMade(t.Context(), "a", "payment", "refund"); err != nil {
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
				assertPaymentStateFinancialResult(t, s, tenant, now, outcome != cardRefundSucceeded)
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
			setup := capability.PaymentEvent{EventID: "setup-completed", ObjectID: "setup-intent", CustomerID: "cus_1", Status: "succeeded", Mode: capability.PaymentTest, Kind: capability.PaymentCardSetup, Created: now.Unix() + 2}
			events := map[string]capability.PaymentEvent{"page": page, "setup": setup}
			for _, name := range order {
				for range 2 {
					if _, err := s.settlePaymentFacts(t.Context(), events[name]); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
				}
			}
			tenant.s.SetTestClock(t, func() time.Time { return now })
			standing, err := tenant.SpendStanding(t.Context())
			if err != nil || !standing.Card.AddedAt.Equal(time.Unix(now.Unix()+2, 0)) {
				t.Fatalf("setup was lost before page association: %+v: %v", standing.Card, err)
			}
		})
	}
}

func TestPaymentStateEveryRefillOutcomePermutation(t *testing.T) {
	for _, order := range deliveryOrders("refused", "success", "failure") {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			s, tenant, now := providerPaymentFixture(t)
			if err := s.inTx(t.Context(), func(tx *storeTx) error {
				return setRefillTx(t.Context(), tx, "a", RefillSettings{Enabled: true, ThresholdCents: 10_000_000, AmountCents: 15_000}, auditAccountActor("owner"))
			}); err != nil {
				t.Fatal(err)
			}
			due, err := collectDueRefills(t.Context(), s.AsOperator())
			if err != nil || len(due) != 1 {
				t.Fatalf("reservation=%+v: %v", due, err)
			}
			paid := refillPaid(now, due[0], "payment")
			failed := paid
			failed.Kind, failed.EventID, failed.Status, failed.DeclineCode = capability.PaymentCardFailed, "failed", "requires_payment_method", "card_declined"
			failed.ChargeID = ""
			for _, name := range order {
				for range 2 {
					if name == "refused" {
						if _, err := s.AsOperator().RecordRefillFailed(t.Context(), due[0], "card_declined"); err != nil {
							t.Fatal(err)
						}
					} else {
						event := paid
						if name == "failure" {
							event = failed
						}
						if _, err := s.settlePaymentFacts(t.Context(), event); err != nil {
							t.Fatalf("%s before or after the refusal: %v", name, err)
						}
					}
				}
			}
			if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 15_000*MicroCreditsPerCent {
				t.Fatalf("successful payment lost across a refusal: %d: %v", balance, err)
			}
			var failure string
			if err := s.db.QueryRow(`SELECT failure FROM credit_checkouts WHERE kind='refill' AND payment_id='payment'`).Scan(&failure); err != nil || failure != "" {
				t.Fatalf("terminal success regressed to failure: %q: %v", failure, err)
			}
		})
	}
}

func assertPaymentStateFinancialResult(t *testing.T, s *Store, tenant *Tenant, now time.Time, refundFailed bool) {
	t.Helper()
	if balance, err := tenant.CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("balance=%d, want zero after warning/refund: %v", balance, err)
	}
	if freeze, err := tenant.CreditFreeze(t.Context()); err != nil || !freeze.Frozen {
		t.Fatalf("warning hold=%+v: %v", freeze, err)
	}
	s.SetTestClock(t, func() time.Time { return now.Add(24 * time.Hour) })
	if due, err := s.AsOperator().DueCardRefunds(t.Context()); err != nil || len(due) != 0 {
		t.Fatalf("refunds=%+v, want none queued again: %v", due, err)
	}
	want := 0
	if refundFailed {
		want = 1
	}
	if failed, err := tenant.FailedCardRefunds(t.Context()); err != nil || len(failed) != want {
		t.Fatalf("failed refunds=%+v, want %d listed: %v", failed, want, err)
	}
}
