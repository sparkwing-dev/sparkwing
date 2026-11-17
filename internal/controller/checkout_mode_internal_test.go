package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// safety: modeCheckout holds the Stripe mode pairing as the hosted service does, refusing another mode or none with
// 421 before charging and naming its mode on every answer; unpaired stands in for a service that does neither.
type modeCheckout struct {
	mode     string
	unpaired bool

	mu      sync.Mutex
	charged int
}

func (f *modeCheckout) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.unpaired {
		w.Header().Set(checkoutModeHeader, f.mode)
		if r.Header.Get(checkoutModeHeader) != f.mode {
			http.Error(w, `{"error":"the controller's Stripe mode must equal this service's, set by BILLING_LIVE"}`, http.StatusMisdirectedRequest)
			return
		}
	}
	f.mu.Lock()
	f.charged++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/internal/refill":
		_, _ = w.Write([]byte(`{"payment_intent":"pi_mode","status":"succeeded"}`))
	case "/internal/refund":
		_, _ = w.Write([]byte(`{"refund":"re_mode","status":"pending"}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *modeCheckout) charges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.charged
}

// A refill or refund reaches Stripe only when the controller and the checkout service run in the same Stripe mode. A
// service in the other mode refuses before charging, and a service that names no mode is not believed; either way the
// refill stays open and the refund queued, both are retried, and the operator gets one misconfiguration alert naming
// the controller's mode.
//
//sparkwing:critical
func TestRefillsAndRefundsMoveMoneyOnlyInTheControllersStripeMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service *modeCheckout
		paired  bool
		charged int
	}{
		{"same mode", &modeCheckout{mode: "test"}, true, 2},
		{"service in live mode", &modeCheckout{mode: "live"}, false, 0},
		{"service names no mode", &modeCheckout{unpaired: true}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, tenant := refillFixture(t)
			engineSQL(t, e.Store, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,created_at) VALUES ('test','a','pi_due','refused',?,1)`, 1_000*store.MicroCreditsPerCent)
			service := tc.service
			srv := httptest.NewServer(service)
			t.Cleanup(srv.Close)
			var logs bytes.Buffer
			cfg := deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PaymentMode: "test"}, license.FeatureMultiTeam)
			worker := NewDeployment(e.Store, cfg, slog.New(slog.NewJSONHandler(&logs, nil))).WithBillingCheckout(srv.URL, "credential")

			worker.refillPass(t.Context())
			worker.refundQueuedPayments(t.Context())

			if got := service.charges(); got != tc.charged {
				t.Fatalf("calls that reached Stripe %d; want %d", got, tc.charged)
			}
			var recorded int
			if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM credit_checkouts WHERE payment_id='pi_mode'`).Scan(&recorded); err != nil {
				t.Fatal(err)
			}
			due, err := e.Store.AsOperator().DueCardRefunds(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			standing, err := tenant.SpendStanding(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			alerts := auditLines(t, &logs, checkoutMisconfiguredMessage)
			if tc.paired {
				if recorded != 1 || len(due) != 0 || len(alerts) != 0 {
					t.Fatalf("paired: refill recorded %d, refunds due %+v, alerts %v; want the refill recorded and the refund made", recorded, due, alerts)
				}
				return
			}
			if recorded != 0 || !standing.Refill.Open || len(due) != 1 || due[0].PaymentIntent != "pi_due" {
				t.Fatalf("mode mismatch: refill recorded %d, refill %+v, refunds due %+v; want the refill open and the refund queued", recorded, standing.Refill, due)
			}
			if len(alerts) != 1 || alerts[0]["alert"] != checkoutMisconfiguredAlert || alerts[0]["payment_mode"] != "test" || alerts[0]["retry_limit"] != "6h0m0s" {
				t.Fatalf("misconfiguration alerts %v; want one naming payment_mode test and retry_limit 6h0m0s", alerts)
			}
		})
	}
}

//sparkwing:critical
func TestUnpairedCheckoutRefusalsResumeAfterRepair(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity} {
		for _, mode := range []string{"live", "", "test"} {
			t.Run(fmt.Sprintf("status_%d_mode_%q", status, mode), func(t *testing.T) {
				e, tenant := refillFixture(t)
				engineSQL(t, e.Store, `INSERT INTO card_refunds(payment_mode,team,payment_intent,reason,amount_micro,created_at) VALUES ('test','a','pi_due','refused',?,1)`, 1_000*store.MicroCreditsPerCent)
				var repaired atomic.Bool
				var mu sync.Mutex
				bodies := map[string][]string{}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					bodies[r.URL.Path] = append(bodies[r.URL.Path], string(body))
					mu.Unlock()
					answerMode := mode
					if repaired.Load() {
						answerMode = "test"
					}
					if answerMode != "" {
						w.Header().Set(checkoutModeHeader, answerMode)
					}
					if !repaired.Load() {
						http.Error(w, `{"error":"provider refused"}`, status)
						return
					}
					if r.Header.Get(checkoutModeHeader) != "test" {
						t.Errorf("request mode %q; want test", r.Header.Get(checkoutModeHeader))
					}
					if r.URL.Path == "/internal/refill" {
						_, _ = w.Write([]byte(`{"payment_intent":"pi_repaired","status":"succeeded"}`))
					} else {
						_, _ = w.Write([]byte(`{"refund":"re_repaired","status":"pending"}`))
					}
				}))
				t.Cleanup(srv.Close)
				var logs bytes.Buffer
				cfg := deploymenttest.Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam), PaymentMode: "test"}, license.FeatureMultiTeam)
				worker := NewDeployment(e.Store, cfg, slog.New(slog.NewJSONHandler(&logs, nil))).WithBillingCheckout(srv.URL, "credential")
				now := time.Unix(1_800_000_000, 0)
				worker.checkout.now = func() time.Time { return now }
				worker.refillPass(t.Context())
				worker.refundQueuedPayments(t.Context())
				standing, err := tenant.SpendStanding(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				due, err := e.Store.AsOperator().DueCardRefunds(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				failed, err := tenant.FailedCardRefunds(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if mode == "test" {
					if standing.Refill.Open || len(due) != 0 || len(failed) != 1 || len(auditLines(t, &logs, checkoutMisconfiguredMessage)) != 0 {
						t.Fatalf("paired refusal: refill %+v, due %v, failed %v, logs %s", standing.Refill, due, failed, &logs)
					}
					return
				}
				if !standing.Refill.Open || !standing.Refill.FailedAt.IsZero() || len(due) != 1 || len(failed) != 0 {
					t.Fatalf("unpaired refusal became final: refill %+v, due %v, failed %v", standing.Refill, due, failed)
				}
				now = now.Add(time.Minute)
				worker.refillPass(t.Context())
				worker.refundQueuedPayments(t.Context())
				alerts := auditLines(t, &logs, checkoutMisconfiguredMessage)
				if len(alerts) != 1 || alerts[0]["payment_mode"] != "test" || alerts[0]["retry_limit"] != "6h0m0s" {
					t.Fatalf("misconfiguration alerts %v; want one test-mode alert", alerts)
				}
				repaired.Store(true)
				now = now.Add(checkoutRetryMaxWait)
				worker.refillPass(t.Context())
				worker.refundQueuedPayments(t.Context())
				var recorded int
				if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM credit_checkouts WHERE payment_id='pi_repaired'`).Scan(&recorded); err != nil {
					t.Fatal(err)
				}
				due, err = e.Store.AsOperator().DueCardRefunds(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				failed, err = tenant.FailedCardRefunds(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if recorded != 1 || len(due) != 0 || len(failed) != 0 || worker.checkout.misconfigured {
					t.Fatalf("repair did not resume work: recorded %d, due %v, failed %v, alert %v", recorded, due, failed, worker.checkout.misconfigured)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, path := range []string{"/internal/refill", "/internal/refund"} {
					requests := bodies[path]
					if len(requests) != 3 || requests[0] != requests[1] || requests[0] != requests[2] {
						t.Fatalf("%s requests %v; want three identical requests across repair", path, requests)
					}
				}
			})
		}
	}
}

func TestBillingPageTransportDoesNotSupplyPaymentMode(t *testing.T) {
	checkout := (&Server{}).WithBillingCheckout("https://checkout.test", "credential").checkout
	checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
		return refillReply(http.StatusOK, `{}`), nil
	})
	var out map[string]any
	err := checkout.post(t.Context(), checkoutPairing{mode: capability.PaymentTest}, "/internal/portal", nil, &out)
	if !errors.Is(err, errCheckoutMisconfigured) || !errors.Is(err, errCheckoutUnavailable) {
		t.Fatalf("headerless answer: %v; want a retryable misconfigured pairing", err)
	}
}

func TestHeaderlessCheckoutNamesTransportAndConfigurationCauses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway, http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			t.Cleanup(srv.Close)
			checkout := (&Server{}).WithBillingCheckout(srv.URL, "credential").checkout
			var out map[string]any
			err := checkout.post(t.Context(), checkoutPairing{mode: capability.PaymentTest}, "/internal/portal", nil, &out)
			if !errors.Is(err, errCheckoutMisconfigured) || !errors.Is(err, errCheckoutUnavailable) {
				t.Fatalf("classification: %v", err)
			}
			for _, want := range []string{"Sparkwing-Payment-Mode", "BILLING_LIVE", "ingress", "502", "--billing-url", "token"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}
