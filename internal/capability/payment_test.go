package capability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
			proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)}, testPaymentScope)
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
				want.ThreeDS, want.Fingerprint = "authenticated", "fp_ch_fixture"
				want.LiabilityShift = true
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
			proof, err := v.VerifyPaymentEvent(context.Background(), p, testPaymentScope)
			if err == nil || proof != nil {
				t.Fatalf("invalid proof accepted: %+v", proof)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("secret leaked")
			}
		})
	}
	valid.Signature += ",v1=" + strings.Repeat("0", 64)
	if _, err := v.VerifyPaymentEvent(context.Background(), valid, testPaymentScope); err != nil {
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

func TestPaidCheckoutReadsThreeDSecureAndCardFromTheSettledCharge(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	body := paymentBody("checkout.session.completed", "paid", false)
	verify := func(objects paymenttest.Provider) (*VerifiedPaymentEvent, []string, error) {
		headers := &paymentHeaders{next: objects}
		provider := newPaymentProvider("sk_test_fixture")
		provider.client.Transport = headers
		v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: provider}
		proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)}, testPaymentScope)
		return proof, headers.version, err
	}
	for _, result := range []string{"authenticated", "attempt_acknowledged", "failed", "processing_error", "exempted", "not_supported", "unknown", ""} {
		proof, versions, err := verify(paymenttest.Provider{}.SettledCheckout("pi_fixture", false, result))
		shifted := result == "authenticated"
		if err != nil || proof == nil || proof.Event().ThreeDS != result || proof.Event().LiabilityShift != shifted || proof.Event().Fingerprint != "fp_ch_fixture" {
			t.Fatalf("%q: proof=%+v err=%v", result, proof, err)
		}
		if len(versions) != 2 || versions[0] != paymentProviderAPIVersion || versions[1] != paymentProviderAPIVersion {
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

type recordsAll bool

func (r recordsAll) RecordsPaymentParent(context.Context, PaymentRoute) (bool, error) {
	return bool(r), nil
}

var testPaymentScope = PaymentScope{Deployment: "staging.sparkwing.dev", Records: recordsAll(true)}

type noProvider struct{ t *testing.T }

func (p noProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	p.t.Errorf("provider read %s before ownership was decided", r.URL.Path)
	return nil, fmt.Errorf("no provider")
}

// Ownership is decided before any provider read: another deployment's stamp is acknowledged without
// reads, an unstamped event resolves through committed records or its parent intent's stamp, and only
// local events reach financial verification.
func TestPaymentOwnershipIsClassifiedBeforeProviderReads(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	stamp := func(body, field string) string {
		return strings.Replace(body, `"metadata":{`, `"metadata":{`+field+`,`, 1)
	}
	ours, theirs := `"sparkwing_deployment":"staging.sparkwing.dev"`, `"sparkwing_deployment":"api.sparkwing.dev"`
	paid := string(paymentBody("checkout.session.completed", "paid", false))
	expired := strings.Replace(stamp(stamp(paid, ours), `"purpose":"card_refill"`), "checkout.session.completed", "checkout.session.expired", 1)
	refund := `{"id":"evt_refund","object":"event","type":"refund.updated","created":900,"livemode":false,"data":{"object":{"id":"re_1","object":"refund","payment_intent":"pi_parent","charge":"ch_parent","status":"succeeded","amount":600,"currency":"usd","livemode":false}}}`
	parent := func(field string) http.RoundTripper {
		return paymenttest.Provider{"/v1/payment_intents/pi_parent": `{"id":"pi_parent","object":"payment_intent","livemode":false,"metadata":{` + field + `}}`}
	}
	for name, tc := range map[string]struct {
		body     string
		recorded bool
		provider http.RoundTripper
		owned    bool
	}{
		"another deployment's checkout":    {stamp(paid, theirs), false, noProvider{t}, false},
		"this deployment's checkout":       {stamp(paid, ours), false, paymenttest.Provider{}.SettledCheckout("pi_fixture", false, ""), true},
		"recorded unstamped checkout":      {paid, true, paymenttest.Provider{}.SettledCheckout("pi_fixture", false, ""), true},
		"unrecorded unstamped checkout":    {paid, false, paymenttest.Provider{"/v1/payment_intents/pi_fixture": `{"id":"pi_fixture","object":"payment_intent","livemode":false}`}, false},
		"refund before its local parent":   {refund, false, parent(ours), true},
		"refund of another deployment":     {refund, false, parent(theirs), false},
		"setup page of another deployment": {stamp(stamp(paid, `"purpose":"card_setup"`), theirs), false, noProvider{t}, false},
		"expired page of this deployment":  {expired, false, noProvider{t}, false},
	} {
		t.Run(name, func(t *testing.T) {
			provider := newPaymentProvider("sk_test_fixture")
			provider.client.Transport = tc.provider
			v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: provider}
			body := []byte(tc.body)
			proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)},
				PaymentScope{Deployment: "staging.sparkwing.dev", Records: recordsAll(tc.recorded)})
			if err != nil || (proof != nil) != tc.owned {
				t.Fatalf("proof=%+v err=%v, want owned=%v", proof, err, tc.owned)
			}
		})
	}
}

func TestCompletedSetupPagesBindTheirIntent(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: newPaymentProvider("sk_test_fixture")}
	v.provider.client.Transport = noProvider{t}
	for _, tc := range []struct{ object, intent string }{
		{`{"id":"cs_setup","object":"checkout.session","mode":"setup","status":"complete","customer":"cus_1","setup_intent":"seti_1","livemode":false,"metadata":{"purpose":"card_setup","sparkwing_deployment":"staging.sparkwing.dev"}}`, "seti_1"},
	} {
		body := []byte(`{"id":"evt_page","object":"event","type":"checkout.session.completed","created":900,"livemode":false,"data":{"object":` + tc.object + `}}`)
		proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)}, PaymentScope{Deployment: "staging.sparkwing.dev", Records: recordsAll(false)})
		if err != nil || proof == nil || proof.Event().Kind != PaymentPageCompleted || proof.Event().IntentID != tc.intent || proof.Event().CustomerID != "cus_1" {
			t.Fatalf("page: %+v %v", proof, err)
		}
	}
}

func TestARecordedObjectStampedElsewhereIsAnIntegrityFault(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	secret := "whsec_fixture_only"
	v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: newPaymentProvider("sk_test_fixture")}
	v.provider.client.Transport = noProvider{t}
	stamp := `"sparkwing_deployment":"dep-another"`
	for name, tc := range map[string]struct{ body []byte }{
		"paid checkout":           {[]byte(strings.Replace(string(paymentBody("checkout.session.completed", "paid", false)), `"metadata":{`, `"metadata":{`+stamp+`,`, 1))},
		"expired setup page":      {verifiedProviderBody(PaymentTest, "checkout.session.expired", `{"id":"cs_fixture","object":"checkout.session","mode":"setup","status":"expired","livemode":false,"metadata":{"purpose":"card_setup",`+stamp+`}}`)},
		"setup without a purpose": {verifiedProviderBody(PaymentTest, "setup_intent.succeeded", `{"id":"cs_fixture","object":"setup_intent","status":"succeeded","livemode":false,"metadata":{`+stamp+`}}`)},
		"failed charge of a page": {verifiedProviderBody(PaymentTest, "payment_intent.payment_failed", `{"id":"cs_fixture","object":"payment_intent","status":"requires_payment_method","livemode":false,"metadata":{`+stamp+`}}`)},
	} {
		proof, err := v.VerifyPaymentEvent(context.Background(), PaymentPresentation{Body: tc.body, Signature: paymentSignature(tc.body, secret, now)}, PaymentScope{Deployment: "dep-ours", Records: recordsAll(true)})
		var conflict *apierr.Error
		if proof != nil || !errors.As(err, &conflict) || conflict.Details["reason"] != PaymentOwnershipReason || conflict.Details["stamp"] != "dep-another" || conflict.Details["object"] != "cs_fixture" {
			t.Fatalf("%s: proof=%+v err=%v, want an ownership conflict before purpose filtering", name, proof, err)
		}
	}
}

func TestRequiresActionProducesRefillFailureProof(t *testing.T) {
	now := time.Now()
	secret := "whsec_fixture_only"
	v := &paymentAuthenticator{mode: PaymentTest, secret: []byte(secret), now: func() time.Time { return now }, provider: newPaymentProvider("sk_test_fixture")}
	body := verifiedProviderBody(PaymentTest, "payment_intent.requires_action", `{"id":"pi_fixture","object":"payment_intent","status":"requires_action","amount":1200,"currency":"usd","customer":"cus_fixture","livemode":false,"metadata":{"purpose":"card_refill"}}`)
	proof, err := v.VerifyPaymentEvent(t.Context(), PaymentPresentation{Body: body, Signature: paymentSignature(body, secret, now)}, testPaymentScope)
	if err != nil || proof == nil {
		t.Fatalf("authentication-required proof=%+v: %v", proof, err)
	}
	if proof.Event().Kind != PaymentCardFailed || proof.Event().DeclineCode != "authentication_required" {
		t.Fatalf("outcome=%+v", proof.Event())
	}
}
