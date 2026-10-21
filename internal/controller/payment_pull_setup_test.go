package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func pullVerifier(t *testing.T, transport http.RoundTripper) capability.PaymentAuthenticator {
	t.Helper()
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "key"), filepath.Join(dir, "secret")
	for path, value := range map[string]string{key: "sk_test_pull", secret: "whsec_pull"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPaymentPullSelectsLastCompletedUsableSetup(t *testing.T) {
	s, _, _ := billingNativeServer(t)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		engineSQL(t, s.store, `INSERT INTO payment_correlations(payment_mode,provider_id,kind,team,customer_id) VALUES ('test',?,'setup','a','cus_owner')`, "seti_"+id)
	}
	billingModelCheckout(t, s.store, tenant, "cs_history", 1000)
	detached, expiredHistory := false, false
	provider := paymenttest.Provider{}.SettledCheckout("pi_history", false, "authenticated")
	verifier := pullVerifier(t, billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if _, ok := provider[r.URL.Path]; ok {
			return provider.RoundTrip(r)
		}
		data := `[]`
		switch r.URL.Path {
		case "/v1/checkout/sessions":
			data = `[{"id":"cs_history","object":"checkout.session","created":1,"livemode":false,"mode":"payment","payment_status":"paid","payment_intent":"pi_history","amount_total":1000,"currency":"usd","metadata":{"sparkwing_deployment":"restore.test"}}]`
		case "/v1/setup_intents":
			data = `[{"id":"seti_A","object":"setup_intent","created":10,"livemode":false,"status":"succeeded","customer":"cus_owner","payment_method":"pm_A","metadata":{"purpose":"card_setup"}},{"id":"seti_B","object":"setup_intent","created":20,"livemode":false,"status":"succeeded","customer":"cus_owner","payment_method":"pm_B","metadata":{"purpose":"card_setup"}}]`
		case "/v1/events":
			if !expiredHistory {
				data = `[{"id":"evt_A","object":"event","type":"setup_intent.succeeded","created":40,"livemode":false,"data":{"object":{"id":"seti_A"}}},{"id":"evt_B","object":"event","type":"setup_intent.succeeded","created":30,"livemode":false,"data":{"object":{"id":"seti_B"}}}]`
			}
		case "/v1/payment_methods/pm_A", "/v1/payment_methods/pm_B":
			customer := `"cus_owner"`
			if detached && r.URL.Path == "/v1/payment_methods/pm_A" {
				customer = `null`
			}
			body := fmt.Sprintf(`{"id":%q,"object":"payment_method","livemode":false,"customer":%s,"type":"card","card":{"fingerprint":%q,"brand":"visa","last4":"4242"}}`, strings.TrimPrefix(r.URL.Path, "/v1/payment_methods/"), customer, "fp_"+filepath.Base(r.URL.Path))
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/v1/refunds":
			if detached {
				data = `[{"id":"re_history","object":"refund","created":50,"livemode":false,"payment_intent":"pi_history","charge":"ch_history","status":"succeeded","amount":600,"currency":"usd"}]`
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
	}))
	s.WithPayments(verifier, s.store.AsOperator()).WithPaymentDeployment("restore.test")
	if err := s.ReconcilePayments(t.Context()); err != nil {
		t.Fatal(err)
	}
	standing, err := tenant.SpendStanding(t.Context())
	if err != nil || standing.Card.PaymentMethod != "pm_A" || standing.Card.AddedAt.Unix() != 40 {
		t.Fatalf("last completed setup not selected: %+v %v", standing.Card, err)
	}
	detached = true
	for sweep := 0; sweep < 2; sweep++ {
		if err := s.ReconcilePayments(t.Context()); err != nil {
			t.Fatal(err)
		}
		standing, err := tenant.SpendStanding(t.Context())
		if err != nil || standing.Card.PaymentMethod != "" || standing.Card.AddedAt.Unix() != 40 || standing.BalanceMicro != 400*store.MicroCreditsPerCent {
			t.Fatalf("detached setup changed selection or blocked refund: %+v %v", standing, err)
		}
		expiredHistory = true
	}
}

type pullMail struct{ messages []mailer.Message }

func (m *pullMail) Send(_ context.Context, message mailer.Message) error {
	m.messages = append(m.messages, message)
	return nil
}

func TestPaymentPullReportsFailedRefill(t *testing.T) {
	e, _ := refillFixture(t)
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('pull-owner','owner@example.test',1,1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','pull-owner','owner',1)`)
	due, err := e.Store.AsOperator().DueRefills(t.Context())
	if err != nil || len(due) != 1 {
		t.Fatalf("due %+v %v", due, err)
	}
	if err := e.Store.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "pi_declined"); err != nil {
		t.Fatal(err)
	}
	verifier := pullVerifier(t, billingPageTransport(func(r *http.Request) (*http.Response, error) {
		data := `[]`
		if r.URL.Path == "/v1/payment_intents" {
			data = `[{"id":"pi_declined","object":"payment_intent","created":1,"livemode":false,"status":"requires_payment_method","customer":"cus_refill","payment_method":"pm_refill","amount":2000,"currency":"usd","last_payment_error":{"decline_code":"insufficient_funds"},"metadata":{"purpose":"card_refill"}}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list","has_more":false,"data":` + data + `}`))}, nil
	}))
	var logs bytes.Buffer
	mail := &pullMail{}
	s := NewDeployment(e.Store, deploymenttest.Team(t), slog.New(slog.NewJSONHandler(&logs, nil))).WithMailer(mail).WithPayments(verifier, e.Store.AsOperator()).WithPaymentDeployment("restore.test")
	for sweep := 0; sweep < 2; sweep++ {
		if err := s.ReconcilePayments(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(logs.String(), `"alert":"refill_failed"`) || len(mail.messages) != 1 || !strings.Contains(mail.messages[0].Subject, "refill failed") {
		t.Fatalf("pull dropped reporting: logs=%s mail=%+v", logs.String(), mail.messages)
	}
}
