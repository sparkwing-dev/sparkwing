package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// A hosted page the checkout service names is followed only on Stripe's own host for that page.
func TestHostedPagesOnlyLeaveForStripe(t *testing.T) {
	serve := func(body string) *billingCheckout {
		return &billingCheckout{url: "https://checkout.test", token: "credential", http: &http.Client{Transport: billingPageTransport(func(*http.Request) (*http.Response, error) {
			return refillReply(http.StatusOK, body), nil
		})}}
	}
	for url, ok := range map[string]bool{
		"https://checkout.stripe.com/c/pay/cs_1":      true,
		"https://billing.stripe.com/p/session/1":      false,
		"https://checkout.stripe.com.evil.test/c/pay": false,
		"https://evil.test/c/pay":                     false,
		"http://checkout.stripe.com/c/pay/cs_1":       false,
		"https://user@checkout.stripe.com/c/pay/cs_1": false,
		"https://checkout.stripe.com:8443/c/pay/cs_1": false,
	} {
		page := &capability.BillingPage{Kind: capability.BillingPageCredits, Team: "a", ReservationID: "r", AmountCents: 1000, ExpiresAt: time.Now().Add(time.Hour)}
		err := serve(`{"id":"cs_1","url":"`+url+`"}`).openReservedPage(t.Context(), "dep", page, "")
		if (err == nil) != ok || err != nil && !errors.Is(err, errCheckoutRefused) {
			t.Fatalf("payment page %s: %v", url, err)
		}
	}
	for url, ok := range map[string]bool{
		"https://billing.stripe.com/p/session/1": true,
		"https://checkout.stripe.com/c/pay/cs_1": false,
		"https://billing.stripe.com.evil.test/p": false,
	} {
		if _, err := serve(`{"url":"`+url+`"}`).portal(t.Context(), "dep", "a", "cus_1"); (err == nil) != ok {
			t.Fatalf("portal page %s: %v", url, err)
		}
	}
}

// A queued refund names the controller's deployment, which the checkout service checks against the payment's stamp.
func TestAQueuedRefundNamesItsDeployment(t *testing.T) {
	var sent map[string]string
	checkout := &billingCheckout{url: "https://checkout.test", token: "credential", http: &http.Client{Transport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		return refillReply(http.StatusOK, `{"refund":"re_1","status":"pending"}`), nil
	})}}
	if _, _, err := checkout.refund(t.Context(), "dep", "a", "pi_1", "duplicate"); err != nil {
		t.Fatal(err)
	}
	if sent["team"] != "a" || sent["deployment"] != "dep" || sent["payment_intent"] != "pi_1" || sent["key"] != "pi_1" || sent["reason"] != "duplicate" {
		t.Fatalf("refund request %v", sent)
	}
}
