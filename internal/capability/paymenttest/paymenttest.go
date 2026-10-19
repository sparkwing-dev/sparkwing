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

// SettledCheckout adds the payment intent and charge a paid checkout reads, with the charge's 3-D Secure result; an empty result reports none.
func (p Provider) SettledCheckout(paymentIntent string, live bool, threeDSecure string) Provider {
	charge := "ch_" + strings.TrimPrefix(paymentIntent, "pi_")
	p["/v1/payment_intents/"+paymentIntent] = fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":%t,"latest_charge":%q}`, paymentIntent, live, charge)
	secure := "null"
	if threeDSecure != "" {
		secure = fmt.Sprintf(`{"result":%q}`, threeDSecure)
	}
	p["/v1/charges/"+charge] = fmt.Sprintf(`{"id":%q,"object":"charge","livemode":%t,"payment_intent":%q,"payment_method_details":{"card":{"fingerprint":"fp_%s","three_d_secure":%s}}}`, charge, live, paymentIntent, charge, secure)
	return p
}
