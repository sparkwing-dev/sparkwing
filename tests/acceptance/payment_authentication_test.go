package acceptance_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// safety: these cells verify the deployed HTTP boundary; ledger atomicity remains a store test.
type paymentLedger struct {
	mu       sync.Mutex
	events   []capability.PaymentEvent
	err      error
	unrecord bool
}

func (l *paymentLedger) RecordsPaymentParent(context.Context, capability.PaymentRoute) (capability.PaymentParentState, error) {
	if l.unrecord {
		return capability.PaymentParentMissing, nil
	}
	return capability.PaymentParentRecorded, nil
}

func (l *paymentLedger) SettlePayment(_ context.Context, proof capability.VerifiedPaymentEvent) (store.PaymentSettlement, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, proof.Event())
	return store.PaymentSettlement{Team: "committed-team", CheckoutID: proof.Event().CheckoutID}, l.err
}

const paymentTestSecret = "whsec_synthetic_acceptance_secret"

func paymentVerifier(t *testing.T, mode capability.PaymentMode) capability.PaymentAuthenticator {
	t.Helper()
	dir := t.TempDir()
	keyFile, secretFile := filepath.Join(dir, "api-key"), filepath.Join(dir, "webhook-secret")
	for path, value := range map[string]string{keyFile: "sk_" + string(mode) + "_synthetic", secretFile: paymentTestSecret} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: mode, APIKeyFile: keyFile, WebhookSecretFile: secretFile, HTTPTransport: paymenttest.Provider{}.SettledCheckout("pi_committed", mode == capability.PaymentLive, "authenticated")})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func paymentBody(mode capability.PaymentMode, kind, id, status string) string {
	return fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"livemode":%t,"data":{"object":{"id":"cs_committed","object":"checkout.session","mode":"payment","livemode":%t,"payment_status":%q,"payment_intent":"pi_committed","currency":"usd","amount_total":2500,"metadata":{"team":"attacker-team","amount_micro":"999999999"}}}}`, id, kind, mode == capability.PaymentLive, mode == capability.PaymentLive, status)
}

func paymentSignature(body string, now time.Time) string {
	stamp := fmt.Sprint(now.Unix())
	mac := hmac.New(sha256.New, []byte(paymentTestSecret))
	_, _ = io.WriteString(mac, stamp+"."+body)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func paymentServer(verifier capability.PaymentAuthenticator, ledger controller.PaymentSettler) *controller.Server {
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithPayments(verifier, ledger)
}

func TestPaymentHTTPAuthenticationBoundary(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		t.Run(string(mode), func(t *testing.T) {
			verifier := paymentVerifier(t, mode)
			for _, tc := range []struct {
				name, kind, status string
				want               capability.PaymentEventKind
			}{
				{"paid checkout", "checkout.session.completed", "paid", capability.PaymentCheckoutPaid},
				{"async success", "checkout.session.async_payment_succeeded", "paid", capability.PaymentCheckoutPaid},
				{"async failure", "checkout.session.async_payment_failed", "unpaid", capability.PaymentCheckoutFailed},
				{"expired checkout", "checkout.session.expired", "unpaid", capability.PaymentCheckoutExpired},
				{"pending checkout", "checkout.session.completed", "unpaid", ""},
				{"unsupported event", "customer.created", "", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ledger := &paymentLedger{}
					body := paymentBody(mode, tc.kind, "evt_verified", tc.status)
					r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe?team=forged", strings.NewReader(body))
					r.Header.Set("Stripe-Signature", paymentSignature(body, time.Now()))
					r.Header.Set("Authorization", "Bearer not-payment-authority")
					w := httptest.NewRecorder()
					paymentServer(verifier, ledger).Handler().ServeHTTP(w, r)
					if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
						t.Fatalf("callback: %d %s", w.Code, w.Body.String())
					}
					if tc.want == "" {
						if len(ledger.events) != 0 {
							t.Fatal("pending or unsupported event reached settlement")
						}
						return
					}
					want := capability.PaymentEvent{EventID: "evt_verified", CheckoutID: "cs_committed", PaymentID: "pi_committed", Currency: "usd", AmountMinor: 2500, Mode: mode, Kind: tc.want}
					if tc.want == capability.PaymentCheckoutPaid {
						want.ThreeDS, want.Fingerprint = "authenticated", "fp_ch_committed"
						want.LiabilityShift = true
					}
					if len(ledger.events) != 1 || ledger.events[0] != want {
						t.Fatalf("settlement events = %+v, want %+v", ledger.events, want)
					}
				})
			}
		})
	}
}

func TestPaymentHTTPRefusals(t *testing.T) {
	verifier := paymentVerifier(t, capability.PaymentTest)
	for _, name := range []string{"missing proof", "wrong proof", "duplicate header", "expired proof", "changed body", "wrong mode", "malformed event", "ledger unavailable", "settlement conflict", "unconfigured", "missing ledger", "oversized body"} {
		t.Run(name, func(t *testing.T) {
			ledger := &paymentLedger{}
			auth := verifier
			body := paymentBody(capability.PaymentTest, "checkout.session.completed", "evt_verified", "paid")
			if name == "wrong mode" {
				body = paymentBody(capability.PaymentLive, "checkout.session.completed", "evt_verified", "paid")
			}
			if name == "malformed event" {
				body = `{}`
			}
			signature := paymentSignature(body, time.Now())
			want := apierr.Unauthenticated
			switch name {
			case "missing proof":
				signature = ""
			case "wrong proof":
				signature = "t=1,v1=wrong"
			case "expired proof":
				signature = paymentSignature(body, time.Now().Add(-time.Hour))
			case "changed body":
				body += " "
			case "wrong mode":
				want = apierr.Forbidden
			case "malformed event":
				want = apierr.BadRequest
			case "ledger unavailable":
				ledger.err = errors.New("private database credentials")
				want = apierr.Internal
			case "settlement conflict":
				ledger.err = &apierr.Error{Code: apierr.Conflict, Message: "payment differs from committed checkout"}
				want = apierr.Conflict
			case "unconfigured":
				auth = nil
				want = apierr.Unavailable
			case "missing ledger":
				want = apierr.Unavailable
			case "oversized body":
				body = strings.Repeat(" ", (1<<20)+1)
				signature = paymentSignature(body, time.Now())
				want = apierr.PayloadTooLarge
			}
			r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer service-credits-or-admin")
			if signature != "" {
				r.Header.Set("Stripe-Signature", signature)
			}
			if name == "duplicate header" {
				r.Header.Add("Stripe-Signature", signature)
			}
			server := paymentServer(auth, ledger)
			if name == "missing ledger" {
				server = paymentServer(auth, nil)
			}
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			var envelope apierr.Envelope
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if w.Code != apierr.Status(want) || envelope.Error == nil || envelope.Error.Code != want || strings.Contains(w.Body.String(), "private database") {
				t.Fatalf("refusal = %d %s, want %s", w.Code, w.Body.String(), want)
			}
			calls := 0
			if ledger.err != nil {
				calls = 1
			}
			if len(ledger.events) != calls {
				t.Fatalf("settlement calls = %d, want %d", len(ledger.events), calls)
			}
		})
	}
}

func TestPaymentHTTPServiceRoutesRemoved(t *testing.T) {
	ledger := &paymentLedger{}
	server := paymentServer(paymentVerifier(t, capability.PaymentTest), ledger)
	handler := server.Handler()
	routes := []struct{ method, path string }{
		{"POST", "/api/v1/credits/grants"},
		{"POST", "/api/v1/credits/reversals"},
		{"POST", "/api/v1/credits/freezes"},
		{"POST", "/api/v1/credits/cards"},
		{"POST", "/api/v1/credits/card-payments"},
		{"POST", "/api/v1/credits/warnings"},
		{"POST", "/api/v1/credits/card-refunds"},
		{"GET", "/api/v1/credits/units"},
		{"GET", "/api/v1/credits/payments/pi_forged"},
	}
	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			for i := range 32 {
				body := fmt.Sprintf(`{"team":"forged-%d","amount_micro":%d}`, i, i+1)
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(body))
				r.Header.Set("Authorization", fmt.Sprintf("Bearer sws_forged_%d", i))
				r.Header.Set("Stripe-Signature", paymentSignature(body, time.Now()))
				if server.RegisteredRoute(r) {
					t.Fatal("retired billing route remains registered")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusNotFound {
					t.Fatalf("retired route: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
	if len(ledger.events) != 0 {
		t.Fatal("retired routes reached payment settlement")
	}
	if !server.RegisteredRoute(httptest.NewRequest(http.MethodPost, "/webhooks/stripe", nil)) {
		t.Fatal("payment callback is missing from the server")
	}
}

func TestPaymentHTTPConcurrentRequestsKeepTheirProof(t *testing.T) {
	ledger := &paymentLedger{}
	handler := paymentServer(paymentVerifier(t, capability.PaymentTest), ledger).Handler()
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			body := paymentBody(capability.PaymentTest, "checkout.session.completed", fmt.Sprintf("evt_%d", i), "paid")
			r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
			r.Header.Set("Stripe-Signature", paymentSignature(body, time.Now()))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Errorf("callback: %d %s", w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	seen := make(map[string]bool)
	for _, event := range ledger.events {
		if seen[event.EventID] {
			t.Fatal("concurrent requests shared a proof")
		}
		seen[event.EventID] = true
	}
	if len(seen) != 24 {
		t.Fatalf("settled %d of 24 requests", len(seen))
	}
}

func TestPaymentHTTPFinancialProviderHandoffProperty(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		verifier := paymentVerifier(t, mode)
		for _, tc := range []struct {
			kind, object string
			want         capability.PaymentEventKind
		}{
			{"refund.updated", `{"id":"re_committed","object":"refund","payment_intent":"pi_committed","charge":"ch_committed","status":"succeeded","amount":500,"currency":"usd"}`, capability.PaymentRefund},
			{"payment_intent.payment_failed", `{"id":"pi_committed","object":"payment_intent","status":"requires_payment_method","amount":500,"currency":"usd","last_payment_error":{"decline_code":"insufficient_funds"}}`, capability.PaymentCardFailed},
			{"charge.dispute.created", `{"id":"dp_committed","object":"dispute","payment_intent":"pi_committed","charge":"ch_committed","status":"needs_response","amount":500,"currency":"usd","reason":"fraudulent"}`, capability.PaymentDisputeOpened},
			{"charge.dispute.closed", `{"id":"dp_committed","object":"dispute","payment_intent":"pi_committed","charge":"ch_committed","status":"lost","amount":500,"currency":"usd","reason":"fraudulent"}`, capability.PaymentDisputeClosed},
		} {
			t.Run(string(mode)+"/"+tc.kind, func(t *testing.T) {
				property := func(metadata string, mutation uint8) bool {
					var object map[string]any
					if json.Unmarshal([]byte(tc.object), &object) != nil {
						return false
					}
					object["metadata"] = map[string]any{"team": metadata, "amount_micro": metadata, "attempt": metadata, "purpose": "card_refill"}
					object["livemode"] = mode == capability.PaymentLive
					encoded, _ := json.Marshal(object)
					body := fmt.Sprintf(`{"id":"evt_provider","object":"event","created":900,"type":%q,"livemode":%t,"data":{"object":%s}}`, tc.kind, mode == capability.PaymentLive, encoded)
					ledger := &paymentLedger{}
					h := paymentServer(verifier, ledger).Handler()
					r := httptest.NewRequest("POST", "https://controller.test/webhooks/stripe?team=forged", strings.NewReader(body))
					r.Header.Set("Stripe-Signature", paymentSignature(body, time.Now()))
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != 204 || len(ledger.events) != 1 {
						t.Logf("HTTP %d %s", w.Code, w.Body)
						return false
					}
					e := ledger.events[0]
					if e.Kind != tc.want || e.PaymentID != "pi_committed" || e.AmountMinor != 500 || e.Currency != "usd" || e.Mode != mode || e.Created != 900 {
						return false
					}
					ledger.events = nil
					signature := paymentSignature(body, time.Now())
					if mutation%2 == 0 {
						body += " "
					} else {
						signature = paymentSignature(body, time.Now().Add(-time.Hour))
					}
					r = httptest.NewRequest("POST", "https://controller.test/webhooks/stripe", strings.NewReader(body))
					r.Header.Set("Stripe-Signature", signature)
					w = httptest.NewRecorder()
					h.ServeHTTP(w, r)
					return w.Code == 401 && len(ledger.events) == 0
				}
				if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// A signed event for another deployment, or for an object this deployment does
// not know and did not stamp, is acknowledged so Stripe stops retrying it, and
// is never settled; this deployment's own unknown object still fails loudly.
func TestPaymentHTTPAcknowledgesEventsThisDeploymentDoesNotOwn(t *testing.T) {
	verifier := paymentVerifier(t, capability.PaymentTest)
	stamped := func(deployment string) string {
		body := paymentBody(capability.PaymentTest, "checkout.session.completed", "evt_"+deployment, "paid")
		return strings.Replace(body, `"metadata":{`, `"metadata":{"sparkwing_deployment":"`+deployment+`",`, 1)
	}
	for name, tc := range map[string]struct {
		body    string
		missing bool
		status  int
		settles int
	}{
		"another deployment":               {stamped("api.sparkwing.dev"), true, http.StatusNoContent, 0},
		"recorded here, stamped elsewhere": {stamped("api.sparkwing.dev"), false, http.StatusInternalServerError, 0},
		"unknown and unstamped":            {paymentBody(capability.PaymentTest, "checkout.session.completed", "evt_unstamped", "paid"), true, http.StatusNoContent, 0},
		"unhandled event type":             {paymentBody(capability.PaymentTest, "customer.created", "evt_customer", "paid"), false, http.StatusNoContent, 0},
		"this deployment's unknown one":    {stamped("staging.sparkwing.dev"), true, http.StatusNotFound, 1},
		"this deployment's known one":      {stamped("staging.sparkwing.dev"), false, http.StatusNoContent, 1},
	} {
		t.Run(name, func(t *testing.T) {
			ledger := &paymentLedger{}
			if tc.missing {
				ledger.err, ledger.unrecord = &apierr.Error{Code: apierr.NotFound, Message: "committed checkout is unavailable"}, true
			}
			server := paymentServer(verifier, ledger).WithPaymentDeployment("staging.sparkwing.dev")
			r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(tc.body))
			r.Header.Set("Stripe-Signature", paymentSignature(tc.body, time.Now()))
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if w.Code != tc.status || len(ledger.events) != tc.settles {
				t.Fatalf("status %d settled %d, want %d and %d: %s", w.Code, len(ledger.events), tc.status, tc.settles, w.Body)
			}
		})
	}
	unsigned := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(stamped("api.sparkwing.dev")))
	unsigned.Header.Set("Stripe-Signature", paymentSignature("other body", time.Now()))
	w := httptest.NewRecorder()
	paymentServer(verifier, &paymentLedger{}).WithPaymentDeployment("staging.sparkwing.dev").Handler().ServeHTTP(w, unsigned)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("badly signed foreign event = %d, want 401", w.Code)
	}
}
