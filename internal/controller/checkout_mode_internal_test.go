package controller

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
			alerts := auditLines(t, &logs, "billing alert: refills and refunds are retried until the pairing is fixed")
			if tc.paired {
				if recorded != 1 || len(due) != 0 || len(alerts) != 0 {
					t.Fatalf("paired: refill recorded %d, refunds due %+v, alerts %v; want the refill recorded and the refund made", recorded, due, alerts)
				}
				return
			}
			if recorded != 0 || !standing.Refill.Open || len(due) != 1 || due[0].PaymentIntent != "pi_due" {
				t.Fatalf("mode mismatch: refill recorded %d, refill %+v, refunds due %+v; want the refill open and the refund queued", recorded, standing.Refill, due)
			}
			if len(alerts) != 1 || alerts[0]["alert"] != checkoutMisconfiguredAlert || alerts[0]["payment_mode"] != "test" {
				t.Fatalf("misconfiguration alerts %v; want one naming payment_mode test", alerts)
			}
		})
	}
}
