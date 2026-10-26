// Package paymenttest answers payment provider reads from fixed objects.
package paymenttest

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Provider maps a provider API path to the JSON object it returns and refuses every other request.
type Provider map[string]string

// RoundTrip serves a GET for a known path and answers anything else with 404.
func (p Provider) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := p[r.URL.Path]
	status := http.StatusOK
	if !ok || r.Method != http.MethodGet || r.URL.Host != "api.stripe.com" {
		status, body = http.StatusNotFound, `{}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// Card details every fixture charge carries, as Stripe sends them; Sparkwing must store none of them.
const (
	CardFingerprint = "fpSentinelCard"
	CardBrand       = "brandsentinel"
	CardLast4       = "4931"
	CardMethod      = "pm_sentinel_card"
	PayerEmail      = "payer-sentinel@example.test"
)

// SettledCheckout adds a payment intent and its charge ch_<intent without pi_>, with the charge's 3-D Secure result; an empty result reports none.
func (p Provider) SettledCheckout(paymentIntent string, live bool, threeDSecure string) Provider {
	charge := "ch_" + strings.TrimPrefix(paymentIntent, "pi_")
	p["/v1/payment_intents/"+paymentIntent] = fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":%t,"latest_charge":%q,"payment_method":%q,"receipt_email":%q}`, paymentIntent, live, charge, CardMethod, PayerEmail)
	secure := "null"
	if threeDSecure != "" {
		secure = fmt.Sprintf(`{"result":%q}`, threeDSecure)
	}
	p["/v1/charges/"+charge] = fmt.Sprintf(`{"id":%q,"object":"charge","livemode":%t,"payment_intent":%q,"payment_method":%q,"billing_details":{"email":%q},"payment_method_details":{"card":{"brand":%q,"last4":%q,"fingerprint":%q,"three_d_secure":%s}}}`,
		charge, live, paymentIntent, CardMethod, PayerEmail, CardBrand, CardLast4, CardFingerprint, secure)
	return p
}
