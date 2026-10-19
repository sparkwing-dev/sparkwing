package capability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
)

func paymentSignature(body []byte, secret string, at time.Time) string {
	timestamp := fmt.Sprint(at.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func paymentBody(kind, paid string, live bool) []byte {
	return []byte(fmt.Sprintf(`{"id":"evt_fixture","object":"event","type":%q,"livemode":%t,"data":{"object":{"id":"cs_fixture","object":"checkout.session","mode":"payment","payment_status":%q,"payment_intent":"pi_fixture","amount_total":1200,"currency":"usd","livemode":%t,"metadata":{"team":"attacker","credits":999999}}}}`, kind, live, paid, live))
}

func TestPaymentProof(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	provider := newPaymentProvider("sk_test_fixture")
	provider.client.Transport = paymenttest.Provider{}.SettledCheckout("pi_fixture", false, "authenticated")
	v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: provider}
	for _, tc := range []struct {
		kind, paid string
		want       PaymentEventKind
	}{
		{"checkout.session.completed", "paid", PaymentCheckoutPaid},
		{"checkout.session.async_payment_succeeded", "paid", PaymentCheckoutPaid},
		{"checkout.session.expired", "unpaid", PaymentCheckoutExpired},
		{"checkout.session.async_payment_failed", "unpaid", PaymentCheckoutFailed},
		{"checkout.session.completed", "unpaid", ""},
		{"customer.created", "paid", ""},
	} {
		t.Run(tc.kind+"/"+tc.paid, func(t *testing.T) {
			body := paymentBody(tc.kind, tc.paid, false)
			proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if proof != nil {
					t.Fatal("pending/unsupported event produced settlement")
				}
				return
			}
			want := PaymentEvent{EventID: "evt_fixture", CheckoutID: "cs_fixture", PaymentID: "pi_fixture", Currency: "usd", AmountMinor: 1200, Mode: PaymentTest, Kind: tc.want}
			if tc.want == PaymentCheckoutPaid {
				want.ThreeDS = "authenticated"
			}
			if proof == nil || proof.Event() != want {
				t.Fatalf("proof=%+v want=%+v", proof, want)
			}
		})
	}
	body := paymentBody("checkout.session.completed", "paid", false)
	valid := PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)}
	for _, tc := range []struct {
		name   string
		change func(*PaymentPresentation)
	}{
		{"missing", func(p *PaymentPresentation) { p.Signature = "" }},
		{"wrong key", func(p *PaymentPresentation) { p.Signature = paymentSignature(body, "wrong", now) }},
		{"stale", func(p *PaymentPresentation) { p.Signature = paymentSignature(body, secret, now.Add(-301*time.Second)) }},
		{"future", func(p *PaymentPresentation) { p.Signature = paymentSignature(body, secret, now.Add(301*time.Second)) }},
		{"ambiguous timestamp", func(p *PaymentPresentation) { p.Signature += fmt.Sprintf(",t=%d", now.Unix()) }},
		{"body changed", func(p *PaymentPresentation) { p.Body = append([]byte(" "), body...) }},
		{"wrong mode", func(p *PaymentPresentation) {
			p.Body = paymentBody("checkout.session.completed", "paid", true)
			p.Signature = paymentSignature(p.Body, secret, now)
		}},
		{"missing payment", func(p *PaymentPresentation) {
			p.Body = []byte(strings.ReplaceAll(string(body), `"pi_fixture"`, `null`))
			p.Signature = paymentSignature(p.Body, secret, now)
		}},
		{"missing mode", func(p *PaymentPresentation) {
			p.Body = []byte(strings.ReplaceAll(string(body), `"livemode":false,`, ``))
			p.Signature = paymentSignature(p.Body, secret, now)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			proof, err := v.VerifyPaymentEvent(context.Background(), p)
			if err == nil || proof != nil {
				t.Fatalf("invalid proof accepted: %+v", proof)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("secret leaked")
			}
		})
	}
	valid.Signature += ",v1=" + strings.Repeat("0", 64)
	if _, err := v.VerifyPaymentEvent(context.Background(), valid); err != nil {
		t.Fatal("rotation signatures refused:", err)
	}
	if (VerifiedPaymentEvent{}).Event() != (PaymentEvent{}) {
		t.Fatal("zero proof carries an event")
	}
}

func TestPaymentPrivateConfiguration(t *testing.T) {
	dir := t.TempDir()
	key, secret := filepath.Join(dir, "api-key"), filepath.Join(dir, "webhook-secret")
	for _, mode := range []PaymentMode{PaymentTest, PaymentLive} {
		if err := os.WriteFile(key, []byte("sk_"+string(mode)+"_fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(secret, []byte("whsec_fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPaymentAuthenticator(PaymentConfig{Mode: mode, APIKeyFile: key, WebhookSecretFile: secret}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewPaymentAuthenticator(PaymentConfig{Mode: PaymentTest, APIKeyFile: key, WebhookSecretFile: secret}); err == nil {
		t.Fatal("live key loaded in test mode")
	}
	if _, err := NewPaymentAuthenticator(PaymentConfig{}); err == nil {
		t.Fatal("unset configuration accepted")
	}
}

type paymentHeaders struct {
	next    http.RoundTripper
	version []string
}

func (h *paymentHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	h.version = append(h.version, r.Header.Get("Stripe-Version"))
	return h.next.RoundTrip(r)
}

func TestPaidCheckoutReadsThreeDSecureFromTheSettledCharge(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	body := paymentBody("checkout.session.completed", "paid", false)
	verify := func(objects paymenttest.Provider) (*VerifiedPaymentEvent, []string, error) {
		headers := &paymentHeaders{next: objects}
		provider := newPaymentProvider("sk_test_fixture")
		provider.client.Transport = headers
		v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: provider}
		proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)})
		return proof, headers.version, err
	}
	for _, result := range []string{"authenticated", "attempt_acknowledged", ""} {
		proof, versions, err := verify(paymenttest.Provider{}.SettledCheckout("pi_fixture", false, result))
		if err != nil || proof == nil || proof.Event().ThreeDS != result {
			t.Fatalf("%q: proof=%+v err=%v", result, proof, err)
		}
		if len(versions) != 2 || versions[0] != PaymentProviderAPIVersion || versions[1] != PaymentProviderAPIVersion {
			t.Fatalf("provider reads used Stripe versions %q", versions)
		}
	}
	foreign := paymenttest.Provider{}.SettledCheckout("pi_fixture", false, "authenticated")
	foreign["/v1/charges/ch_fixture"] = strings.Replace(foreign["/v1/charges/ch_fixture"], `"payment_intent":"pi_fixture"`, `"payment_intent":"pi_other"`, 1)
	uncharged := paymenttest.Provider{}.SettledCheckout("pi_fixture", false, "authenticated")
	uncharged["/v1/payment_intents/pi_fixture"] = `{"id":"pi_fixture","object":"payment_intent","livemode":false,"latest_charge":null}`
	for name, objects := range map[string]paymenttest.Provider{"foreign charge": foreign, "no charge": uncharged, "unreachable": {}} {
		if proof, _, err := verify(objects); err == nil || proof != nil {
			t.Fatalf("%s: accepted %+v", name, proof)
		}
	}
}
