package store

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func deliveryOrders(events ...string) [][]string {
	if len(events) <= 1 {
		return [][]string{events}
	}
	var out [][]string
	for i, first := range events {
		rest := append(append([]string{}, events[:i]...), events[i+1:]...)
		for _, tail := range deliveryOrders(rest...) {
			out = append(out, append([]string{first}, tail...))
		}
	}
	return out
}

// Every delivery order of a payment, its full refund and an actionable early fraud warning ends with the payment's
// credit gone, at most one refund owed, and the team held exactly when a warning arrived; replaying every event
// changes nothing.
func TestPaymentConsequencesSurviveEveryDeliveryOrder(t *testing.T) {
	type fixture struct {
		s      *Store
		tenant *Tenant
		pay    capability.PaymentEvent
		now    time.Time
	}
	kinds := map[string]func(t *testing.T) fixture{
		"add funds": func(t *testing.T) fixture {
			s, pay := paymentFixture(t, capability.PaymentTest)
			tenant, err := s.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			return fixture{s: s, tenant: tenant, pay: pay, now: s.now()}
		},
		"refill": func(t *testing.T) fixture {
			s, tenant, now := providerPaymentFixture(t)
			return fixture{s: s, tenant: tenant, pay: providerRefillEvent(t, s, now, capability.PaymentCardPaid), now: now}
		},
	}
	sets := [][]string{{"pay", "refund"}, {"pay", "warning"}, {"pay", "refund", "warning"}}
	for kind, setup := range kinds {
		for _, set := range sets {
			for _, order := range deliveryOrders(set...) {
				t.Run(kind+"/"+strings.Join(order, "-"), func(t *testing.T) {
					f := setup(t)
					baseline, err := f.tenant.CreditBalanceMicro(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					refund := providerPaymentEvent(f.now, capability.PaymentRefund)
					refund.Status, refund.AmountMinor = cardRefundSucceeded, f.pay.AmountMinor
					warning := providerPaymentEvent(f.now, capability.PaymentWarningEvent)
					warning.AmountMinor, warning.Currency, warning.Actionable = 0, "", true
					events := map[string]capability.PaymentEvent{"pay": f.pay, "refund": refund, "warning": warning}
					for range 2 {
						for _, name := range order {
							if _, err := f.s.settlePaymentFacts(t.Context(), events[name]); err != nil {
								t.Fatalf("%s: %v", name, err)
							}
						}
					}
					if balance, err := f.tenant.CreditBalanceMicro(t.Context()); err != nil || balance != baseline {
						t.Fatalf("balance = %d, want %d, the payment's credit gone: %v", balance, baseline, err)
					}
					warned := strings.Contains(strings.Join(order, " "), "warning")
					refunded := strings.Contains(strings.Join(order, " "), "refund")
					if freeze, err := f.tenant.CreditFreeze(t.Context()); err != nil || freeze.Frozen != warned {
						t.Fatalf("held = %+v, want held %t: %v", freeze, warned, err)
					}
					f.s.SetTestClock(t, func() time.Time { return f.now.Add(time.Hour) })
					due, err := f.s.AsOperator().DueCardRefunds(t.Context())
					want := 0
					if warned && !refunded {
						want = 1
					}
					if err != nil || len(due) != want {
						t.Fatalf("refunds owed = %+v, want %d: %v", due, want, err)
					}
				})
			}
		}
	}
}

// A replay of the identical signed warning after its refund completed, and after a provider read now reports
// other card facts, is the same event.
func TestAWarningReplaysAfterItsRefundCompleted(t *testing.T) {
	s, pay := paymentFixture(t, capability.PaymentTest)
	now := s.now()
	warning := providerPaymentEvent(now, capability.PaymentWarningEvent)
	warning.AmountMinor, warning.Currency, warning.Actionable = 0, "", true
	refund := providerPaymentEvent(now, capability.PaymentRefund)
	refund.Status, refund.AmountMinor = cardRefundSucceeded, pay.AmountMinor
	for _, e := range []capability.PaymentEvent{pay, warning, refund} {
		if _, err := s.settlePaymentFacts(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	replay := warning

	got, err := s.settlePaymentFacts(t.Context(), replay)
	if err != nil || !got.Replayed {
		t.Fatalf("replay = %+v, %v; want the recorded warning", got, err)
	}
	changed := warning
	changed.Actionable = false
	if _, err := s.settlePaymentFacts(t.Context(), changed); err == nil {
		t.Fatal("a different signed warning under the same event id was accepted")
	}
}
