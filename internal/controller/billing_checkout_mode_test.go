package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/license"
)

func TestBillingPagesRequireTheControllersPaymentMode(t *testing.T) {
	for _, mode := range []capability.PaymentMode{capability.PaymentTest, capability.PaymentLive} {
		for _, route := range []string{"checkout", "card"} {
			t.Run(string(mode)+"/"+route, func(t *testing.T) {
				s, session, _ := billingNativeServer(t)
				s.deployment = deploymenttest.Parse(t, deployment.Inputs{Kind: "team", PaymentMode: string(mode)}, license.FeatureMultiTeam)
				if _, err := s.store.AsOperator().ConfigureDeployment(t.Context(), mode, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				var sessionID string
				s.checkout.http.Transport = billingPageTransport(func(*http.Request) (*http.Response, error) {
					customer := ""
					if route == "card" {
						customer = "cus_mode"
					}
					return refillReply(http.StatusOK, fmt.Sprintf(`{"id":%q,"customer":%q,"url":"https://checkout.stripe.com/page"}`, sessionID, customer)), nil
				})
				h := s.Handler()
				call := func(want int) {
					t.Helper()
					body := ""
					if route == "checkout" {
						body = `{"amount_cents":2000}`
					}
					r := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/team/billing/"+route, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Origin", "https://controller.test")
					r.Header.Set("Idempotency-Key", "mode-check")
					if err := ConsoleFixtureRequest(s, r, session); err != nil {
						t.Fatal(err)
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != want || want != http.StatusOK && strings.Contains(w.Body.String(), "https://checkout.stripe.com/page") {
						t.Fatalf("session %q: %d %s; want %d", sessionID, w.Code, w.Body, want)
					}
				}
				sessionID = "cs_live_wrong"
				if mode == capability.PaymentLive {
					sessionID = "cs_test_wrong"
				}
				call(http.StatusServiceUnavailable)
				sessionID = "cs_unknown"
				call(http.StatusServiceUnavailable)
				sessionID = "cs_" + string(mode) + "_"
				call(http.StatusServiceUnavailable)
				sessionID = "cs_" + string(mode) + "_correct"
				call(http.StatusOK)
				call(http.StatusOK)
			})
		}
	}
}
