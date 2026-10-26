package controller

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func stolenCardSession(id, payment string) string {
	return fmt.Sprintf(`{"id":%q,"object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":%q,"amount_total":2000,"currency":"usd"}`, id, payment)
}

func stolenCardWarning(id, payment string) string {
	return fmt.Sprintf(`{"id":%q,"object":"radar.early_fraud_warning","livemode":false,"actionable":true,"charge":"ch_%s","payment_intent":%q}`, id, payment[len("pi_"):], payment)
}

// An early fraud warning holds only the team whose payment it names: a spent payment's credit is taken back into
// debt, the team is held and the refund is queued once, while another team paying with the same card is untouched,
// because Stripe Radar, not Sparkwing, connects one card across teams. A warning that arrives before its payment
// settles refuses that payment and alerts the operator. The identical signed warning replays after its refund.
func TestAnEarlyFraudWarningHoldsOnlyTheTeamItsPaymentFunded(t *testing.T) {
	e, a := engineRuntimeStore(t)
	s := e.Store
	engineSQL(t, s, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, s, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
	b, err := s.ForTeam(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	provider := paymenttest.Provider{}
	for _, payment := range []string{"pi_a", "pi_b", "pi_warned", "pi_returned"} {
		provider.SettledCheckout(payment, false, "")
	}
	deployment, err := s.AsOperator().ConfigureDeployment(t.Context(), "", store.DefaultFreeTeamSlots, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server {
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		return s.WithPaymentDeployment(deployment)
	})
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
	tier := func(tenant *store.Tenant) string {
		t.Helper()
		standing, err := tenant.BillingStanding(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return standing.Tier
	}
	refunds := func() map[string]string {
		t.Helper()
		due, err := s.AsOperator().DueCardRefunds(t.Context())
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
	if balance(a) != paid || balance(b) != paid {
		t.Fatalf("balances a=%d b=%d; want both teams funded", balance(a), balance(b))
	}

	engineSQL(t, s, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,kind,seconds,amount_micro,charged_at) VALUES ('spent','a','run','one','cloud','usage',60,?,?)`, paid, time.Now().UnixNano())
	for _, id := range []string{"evt_warning", "evt_warning_again"} {
		accept(id, "radar.early_fraud_warning.created", stolenCardWarning("issfr_a", "pi_a"))
	}
	if balance(a) != -paid || tier(a) != "Held" {
		t.Fatalf("after warning: balance %d tier %s; want the spent credit taken back as %d and the team held", balance(a), tier(a), -paid)
	}
	if balance(b) != paid || tier(b) == "Held" {
		t.Fatalf("the warning reached another team: balance %d tier %s", balance(b), tier(b))
	}
	if got := refunds(); len(got) != 1 || got["a/pi_a"] != store.CardRefusedWarning {
		t.Fatalf("refunds %v; want the warned payment queued once", got)
	}
	accept("evt_refunded", "refund.updated", `{"id":"re_a","object":"refund","payment_intent":"pi_a","charge":"ch_a","status":"succeeded","amount":2000,"currency":"usd","livemode":false}`)
	if balance(a) != -paid {
		t.Fatalf("balance after the refund = %d; want the warning's reversal not repeated", balance(a))
	}
	accept("evt_warning", "radar.early_fraud_warning.created", stolenCardWarning("issfr_a", "pi_a"))

	billingModelCheckout(t, s, b, "cs_warned", 2000)
	// A warning that precedes its payment names this deployment, because no committed record names the payment yet.
	accept("evt_warned_first", "radar.early_fraud_warning.created", strings.TrimSuffix(stolenCardWarning("issfr_warned", "pi_warned"), "}")+fmt.Sprintf(`,"metadata":{"sparkwing_deployment":%q}}`, deployment))
	accept("evt_warned_paid", "checkout.session.completed", stolenCardSession("cs_warned", "pi_warned"))
	if got := refunds(); got["b/pi_warned"] != store.CardRefusedWarning || balance(b) != paid || tier(b) != "Held" {
		t.Fatalf("refunds %v balance %d tier %s; want the warned payment refused and its team held", got, balance(b), tier(b))
	}
	if !strings.Contains(logs.String(), `"alert":"card_payment_refused","reason":"warning"`) {
		t.Fatalf("the webhook's refusal raised no alert: %s", logs.String())
	}

	billingModelCheckout(t, s, b, "cs_returned", 2000)
	accept("evt_returned", "checkout.session.completed", stolenCardSession("cs_returned", "pi_returned"))
	accept("evt_returned_refund", "refund.updated", `{"id":"re_returned","object":"refund","payment_intent":"pi_returned","charge":"ch_returned","status":"succeeded","amount":2000,"currency":"usd","livemode":false}`)
	accept("evt_returned_warning", "radar.early_fraud_warning.created", stolenCardWarning("issfr_returned", "pi_returned"))
	if got := refunds(); got["b/pi_returned"] != "" || balance(b) != paid {
		t.Fatalf("refunds %v, balance %d; want a charge already refunded reversed but not refunded again", got, balance(b))
	}
}
