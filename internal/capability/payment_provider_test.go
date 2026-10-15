package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

type paymentTransport func(*http.Request) (*http.Response, error)

func (f paymentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerVerifier(t *testing.T, mode PaymentMode) (*paymentAuthenticator, *int) {
	t.Helper()
	calls := new(int)
	v := &paymentAuthenticator{mode: mode, secret: []byte("whsec_fixture"), now: func() time.Time { return time.Unix(1000, 0) }, provider: newPaymentProvider("sk_" + string(mode) + "_private")}
	v.provider.client.Transport = paymentTransport(func(r *http.Request) (*http.Response, error) {
		*calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.stripe.com" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer sk_"+string(mode)+"_private" {
			t.Fatalf("unsafe provider request: %s %s", r.Method, r.URL)
		}
		var body string
		switch r.URL.Path {
		case "/v1/payment_methods/pm_card":
			body = fmt.Sprintf(`{"id":"pm_card","object":"payment_method","livemode":%t,"customer":"cus_owner","type":"card","card":{"fingerprint":"card-fingerprint","brand":"visa","last4":"4242"}}`, mode == PaymentLive)
		case "/v1/charges/ch_paid":
			body = fmt.Sprintf(`{"id":"ch_paid","object":"charge","livemode":%t,"payment_intent":"pi_paid","payment_method_details":{"card":{"fingerprint":"card-fingerprint"}}}`, mode == PaymentLive)
		default:
			t.Fatalf("unexpected provider path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return v, calls
}

func verifiedProviderBody(mode PaymentMode, kind, object string) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_verified","object":"event","type":%q,"created":900,"livemode":%t,"data":{"object":%s}}`, kind, mode == PaymentLive, object))
}

func financialObjects(mode PaymentMode) map[string]string {
	live := fmt.Sprintf(`,"livemode":%t}`, mode == PaymentLive)
	return map[string]string{
		"setup_intent.succeeded":            `{"id":"seti_setup","object":"setup_intent","status":"succeeded","customer":"cus_owner","payment_method":"pm_card"` + live,
		"payment_intent.succeeded":          `{"id":"pi_paid","object":"payment_intent","status":"succeeded","amount":1200,"amount_received":1200,"currency":"usd","latest_charge":"ch_paid"` + live,
		"payment_intent.payment_failed":     `{"id":"pi_failed","object":"payment_intent","status":"requires_payment_method","amount":1200,"currency":"usd","last_payment_error":{"decline_code":"insufficient_funds"}` + live,
		"refund.created":                    `{"id":"re_refund","object":"refund","payment_intent":"pi_paid","charge":"ch_paid","status":"pending","amount":600,"currency":"usd"` + live,
		"refund.updated":                    `{"id":"re_refund","object":"refund","payment_intent":{"id":"pi_paid"},"charge":{"id":"ch_paid"},"status":"succeeded","amount":600,"currency":"usd"` + live,
		"refund.failed":                     `{"id":"re_refund","object":"refund","payment_intent":"pi_paid","charge":"ch_paid","status":"failed","amount":600,"currency":"usd"` + live,
		"radar.early_fraud_warning.created": `{"id":"issfr_warning","object":"radar.early_fraud_warning","payment_intent":"pi_paid","charge":"ch_paid","actionable":true` + live,
		"radar.early_fraud_warning.updated": `{"id":"issfr_warning","object":"radar.early_fraud_warning","payment_intent":"pi_paid","charge":"ch_paid","actionable":false` + live,
		"charge.dispute.created":            `{"id":"dp_dispute","object":"dispute","payment_intent":"pi_paid","charge":"ch_paid","status":"needs_response","amount":1200,"currency":"usd","reason":"fraudulent"` + live,
		"charge.dispute.closed":             `{"id":"dp_dispute","object":"dispute","payment_intent":"pi_paid","charge":"ch_paid","status":"won","amount":1200,"currency":"usd","reason":"fraudulent"` + live,
	}
}

func TestPaymentProviderOutcomes(t *testing.T) {
	for _, mode := range []PaymentMode{PaymentTest, PaymentLive} {
		for kind, object := range financialObjects(mode) {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				v, calls := providerVerifier(t, mode)
				body := verifiedProviderBody(mode, kind, object)
				proof, err := v.VerifyPaymentEvent(t.Context(), PaymentPresentation{Body: body, Signature: paymentSignature(body, string(v.secret), v.now())})
				if err != nil || proof == nil {
					t.Fatalf("proof=%v err=%v", proof, err)
				}
				e := proof.Event()
				if e.EventID != "evt_verified" || e.ObjectID == "" || e.Created != 900 || e.Mode != mode || e.CheckoutID != "" || e.Kind == "" {
					t.Fatalf("event %+v", e)
				}
				if kind == "setup_intent.succeeded" && (e.CustomerID != "cus_owner" || e.MethodID != "pm_card" || e.Brand != "visa" || e.Last4 != "4242" || e.Fingerprint != "card-fingerprint" || *calls != 1) {
					t.Fatalf("setup %+v calls=%d", e, *calls)
				}
				if kind == "payment_intent.payment_failed" && (e.PaymentID != "pi_failed" || e.DeclineCode != "insufficient_funds" || *calls != 0) {
					t.Fatalf("failure %+v", e)
				}
				if strings.HasPrefix(kind, "refund.") && (e.AmountMinor != 600 || e.PaymentID != "pi_paid" || e.ChargeID != "ch_paid" || *calls != 0) {
					t.Fatalf("refund %+v", e)
				}
				if kind == "payment_intent.succeeded" && (e.AmountMinor != 1200 || e.Fingerprint != "card-fingerprint" || *calls != 1) {
					t.Fatalf("paid %+v", e)
				}
			})
		}
	}
}

func TestPaymentProviderProofProperty(t *testing.T) {
	for kind, object := range financialObjects(PaymentTest) {
		t.Run(kind, func(t *testing.T) {
			property := func(metadata string, mutation uint8) bool {
				v, calls := providerVerifier(t, PaymentTest)
				var raw map[string]any
				if json.Unmarshal([]byte(object), &raw) != nil {
					return false
				}
				raw["metadata"] = map[string]any{"team": metadata, "credits": metadata, "charge": metadata, "attempt": metadata, "fingerprint": metadata}
				encoded, _ := json.Marshal(raw)
				body := verifiedProviderBody(PaymentTest, kind, string(encoded))
				proof, err := v.VerifyPaymentEvent(t.Context(), PaymentPresentation{Body: body, Signature: paymentSignature(body, string(v.secret), v.now())})
				if err != nil || proof == nil {
					return false
				}
				original := proof.Event()
				*calls = 0
				switch mutation % 5 {
				case 0:
					raw["livemode"] = true
				case 1:
					raw["id"] = ""
				case 2:
					raw["object"] = "checkout.session"
				case 3:
					raw["payment_intent"], raw["customer"] = nil, nil
				case 4:
					encoded, _ = json.Marshal(raw)
					unsigned := verifiedProviderBody(PaymentTest, kind, string(encoded))
					unsigned = append(unsigned, ' ')
					p, e := v.VerifyPaymentEvent(t.Context(), PaymentPresentation{Body: unsigned, Signature: paymentSignature(body, string(v.secret), v.now())})
					return e != nil && p == nil && *calls == 0
				}
				encoded, _ = json.Marshal(raw)
				body = verifiedProviderBody(PaymentTest, kind, string(encoded))
				p, e := v.VerifyPaymentEvent(t.Context(), PaymentPresentation{Body: body, Signature: paymentSignature(body, string(v.secret), v.now())})
				if mutation%5 == 3 && strings.HasPrefix(kind, "payment_intent.") {
					return e == nil && p != nil && p.Event() == original
				}
				return e != nil && p == nil && *calls == 0
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPaymentProviderRefusals(t *testing.T) {
	for _, bad := range []string{"mode", "identity", "relation", "malformed", "redirect", "backend", "customer", "oversized"} {
		t.Run(bad, func(t *testing.T) {
			v, _ := providerVerifier(t, PaymentTest)
			v.provider.client.Transport = paymentTransport(func(*http.Request) (*http.Response, error) {
				body := `{"id":"pm_card","object":"payment_method","livemode":false,"customer":"cus_owner","type":"card","card":{"fingerprint":"card-fingerprint","brand":"visa","last4":"4242"}}`
				status := 200
				switch bad {
				case "mode":
					body = strings.ReplaceAll(body, `false`, `true`)
				case "identity":
					body = strings.ReplaceAll(body, `pm_card`, `pm_other`)
				case "relation":
					body = strings.ReplaceAll(body, `payment_method`, `charge`)
				case "malformed":
					body = `{"error":"private-provider-diagnostic"}`
				case "redirect":
					status = 302
				case "backend":
					return nil, fmt.Errorf("private-provider-diagnostic")
				case "customer":
					body = strings.ReplaceAll(body, `cus_owner`, `cus_other`)
				case "oversized":
					body = strings.Repeat("x", (1<<20)+1)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Location": []string{"https://attacker.invalid"}}}, nil
			})
			body := verifiedProviderBody(PaymentTest, "setup_intent.succeeded", financialObjects(PaymentTest)["setup_intent.succeeded"])
			p, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, string(v.secret), v.now())})
			if err == nil || p != nil || strings.Contains(err.Error(), "private-provider") {
				t.Fatalf("proof=%+v err=%v", p, err)
			}
		})
	}
}
