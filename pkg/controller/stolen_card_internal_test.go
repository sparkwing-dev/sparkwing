package controller

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func stolenCardSession(id, payment string) string {
	return fmt.Sprintf(`{"id":%q,"object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":%q,"amount_total":2000,"currency":"usd"}`, id, payment)
}

func stolenCardWarning(id, payment string) string {
	return fmt.Sprintf(`{"id":%q,"object":"radar.early_fraud_warning","livemode":false,"actionable":true,"charge":"ch_%s","payment_intent":%q}`, id, payment[len("pi_"):], payment)
}

// A card's first successful payment pins it to that team, a payment by it for another team is refused and queued
// for refund, and an early fraud warning on a spent payment takes the credit back into debt, holds the team,
// queues the refund once, forgets the card and refuses the card's next payment.
func TestAStolenCardIsPinnedRefusedAndRefundedOnWarning(t *testing.T) {
	e, a := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, s, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
	b, err := s.ForTeam(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	provider := paymenttest.Provider{}
	for _, payment := range []string{"pi_a", "pi_b", "pi_again"} {
		provider.Charge(payment, false, "authenticated", "fp_stolen", false)
	}
	provider.Charge("pi_returned", false, "authenticated", "fp_returned", true)
	_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s })
	accept := func(id, kind, object string) {
		t.Helper()
		if w := deliver(id, kind, object); w.Code != http.StatusNoContent {
			t.Fatalf("%s = %d %s", id, w.Code, w.Body)
		}
	}
	balance := func(tenant *store.Tenant) int64 {
		t.Helper()
		micro, err := tenant.CreditBalanceMicro(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return micro
	}
	refunds := func() map[string]string {
		t.Helper()
		due, err := s.AsOperator().DueCardRefunds(t.Context(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range due {
			out[string(r.Team)+"/"+r.PaymentIntent] = r.Reason
		}
		return out
	}
	paid := int64(2000 * store.MicroCreditsPerCent)

	billingModelCheckout(t, s, a, "cs_a", 2000)
	accept("evt_a", "checkout.session.completed", stolenCardSession("cs_a", "pi_a"))
	billingModelCheckout(t, s, b, "cs_b", 2000)
	accept("evt_b", "checkout.session.completed", stolenCardSession("cs_b", "pi_b"))
	if balance(a) != paid || balance(b) != 0 {
		t.Fatalf("balances a=%d b=%d; want the first team funded and the card refused for the second", balance(a), balance(b))
	}
	if got := refunds(); len(got) != 1 || got["b/pi_b"] != store.CardRefusedPinned {
		t.Fatalf("refunds %v; want the second team's payment queued as card_pinned", got)
	}

	engineSQL(t, s, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('spent','a','run','one','cloud','usage',60,?,?)`, paid, time.Now().UnixNano())
	engineSQL(t, s, `UPDATE teams SET card_customer='cus_a',card_payment_method='pm_stolen',card_fingerprint='fp_stolen',card_brand='visa',card_last4='4242' WHERE name='a'`)
	for _, id := range []string{"evt_warning", "evt_warning_again"} {
		accept(id, "radar.early_fraud_warning.created", stolenCardWarning("issfr_a", "pi_a"))
	}
	if balance(a) != -paid {
		t.Fatalf("balance after warning = %d; want the spent credit taken back as %d", balance(a), -paid)
	}
	standing, err := a.BillingStanding(t.Context(), time.Now())
	if err != nil || standing.Tier != "Held" {
		t.Fatalf("standing after warning = %+v, %v; want Held", standing, err)
	}
	if spend, err := a.SpendStanding(t.Context(), time.Now()); err != nil || spend.Card.PaymentMethod != "" {
		t.Fatalf("saved card = %+v, %v; want the warned card forgotten", spend.Card, err)
	}
	if got := refunds(); len(got) != 2 || got["a/pi_a"] != store.CardRefusedWarning {
		t.Fatalf("refunds %v; want the warned payment queued once", got)
	}
	accept("evt_refunded", "refund.updated", `{"id":"re_a","object":"refund","payment_intent":"pi_a","charge":"ch_a","status":"succeeded","amount":2000,"currency":"usd","livemode":false}`)
	if balance(a) != -paid {
		t.Fatalf("balance after the refund = %d; want the warning's reversal not repeated", balance(a))
	}
	accept("evt_refused_refund", "refund.updated", `{"id":"re_b","object":"refund","payment_intent":"pi_b","charge":"ch_b","status":"succeeded","amount":2000,"currency":"usd","livemode":false}`)
	if balance(b) != 0 {
		t.Fatalf("refused payment's refund moved the balance to %d", balance(b))
	}

	billingModelCheckout(t, s, a, "cs_again", 2000)
	accept("evt_again", "checkout.session.completed", stolenCardSession("cs_again", "pi_again"))
	if balance(a) != -paid {
		t.Fatalf("blocked card's payment granted: balance %d", balance(a))
	}
	if got := refunds(); got["a/pi_again"] != store.CardRefusedWarning {
		t.Fatalf("refunds %v; want the blocked card's payment refused and queued", got)
	}

	billingModelCheckout(t, s, b, "cs_returned", 2000)
	accept("evt_returned", "checkout.session.completed", stolenCardSession("cs_returned", "pi_returned"))
	accept("evt_returned_warning", "radar.early_fraud_warning.created", stolenCardWarning("issfr_returned", "pi_returned"))
	if got := refunds(); got["b/pi_returned"] != "" || balance(b) != 0 {
		t.Fatalf("refunds %v, balance %d; want a charge already refunded reversed but not refunded again", got, balance(b))
	}
}
