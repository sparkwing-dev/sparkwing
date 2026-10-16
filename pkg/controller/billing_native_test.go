package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func billingNativeServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := New(e.Store, nil).WithSecretsCipher(cipher)
	engineSQL(t, s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('owner','owner@example.test',1,1)`)
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	session, csrf, _, err := s.store.CreateAccountSession(t.Context(), store.Account{ID: "owner"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a'`)
	if err := s.store.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	s.WithLicense(license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{Features: []string{license.FeatureMultiTeam, license.FeatureMetering}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}), pub, now, nil)).WithBillingCheckout("https://checkout.test", "credential")
	return s, session, csrf
}

func TestBillingModelProductionPagesAndBudget(t *testing.T) {
	s, session, csrf := billingNativeServer(t)
	calls := 0
	s.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["team"] != "a" || body["amount_cents"] != float64(2000) || body["key"] == "" {
			t.Fatalf("provider: %v", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"url":"https://checkout.test/page","id":"cs_native","expires_at":` + strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10) + `}`))}, nil
	})
	h := s.Handler()
	call := func(method, path, body string, status int) {
		t.Helper()
		r := httptest.NewRequest(method, "https://controller.test/api/v1/team/billing/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set("Idempotency-Key", "native-"+path)
		r.Header.Set(csrfHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: session})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
	}
	call("POST", "checkout", `{"amount_cents":2000}`, 200)
	call("POST", "checkout", `{"amount_cents":2000}`, 200)
	call("POST", "checkout", `{"amount_cents":2001}`, 409)
	call("PUT", "budget", `{"budget_cents":2500}`, 204)
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
	var budget int64
	if err := s.store.DB().QueryRow(`SELECT billing_budget_cents FROM teams WHERE name='a'`).Scan(&budget); err != nil || budget != 2500 {
		t.Fatalf("budget=%d: %v", budget, err)
	}
}

func TestBillingModelProductionCardPrepayAndRecoverySettlement(t *testing.T) {
	s, session, csrf := billingNativeServer(t)
	key, secret := filepath.Join(t.TempDir(), "key"), filepath.Join(t.TempDir(), "secret")
	for path, value := range map[string]string{key: "sk_test_fixture", secret: "whsec_fixture"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: billingPageTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/v1/payment_methods/pm_native":
			body = `{"id":"pm_native","object":"payment_method","livemode":false,"customer":"cus_native","type":"card","card":{"fingerprint":"fp_native","brand":"visa","last4":"4242"}}`
		case "/v1/charges/ch_native":
			body = `{"id":"ch_native","object":"charge","livemode":false,"payment_intent":"pi_recovery","payment_method_details":{"card":{"fingerprint":"fp_native"}}}`
		default:
			t.Fatalf("provider lookup %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	s.WithPayments(verifier, s.store)
	counts := map[string]int{}
	s.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		counts[r.URL.Path]++
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if in["team"] != "a" {
			t.Fatalf("provider team=%v", in)
		}
		body := ""
		switch r.URL.Path {
		case "/internal/card-setup":
			if in["setup_id"] != in["key"] || in["email"] != "owner@example.test" {
				t.Fatalf("setup=%v", in)
			}
			body = `{"id":"cs_setup","setup_intent":"seti_native","customer":"cus_native","url":"https://checkout.test/setup"}`
		case "/internal/checkout":
			body = `{"id":"cs_prepay","url":"https://checkout.test/prepay"}`
		case "/internal/charge":
			if in["amount_cents"] != float64(600) || in["customer"] != "cus_native" || in["payment_method"] != "pm_native" {
				t.Fatalf("charge=%v", in)
			}
			status := "created"
			if counts[r.URL.Path] > 1 {
				status = "processing"
			}
			body = fmt.Sprintf(`{"payment_intent":"pi_auto","status":%q}`, status)
		case "/internal/charge-checkout":
			if in["amount_cents"] != float64(600) || in["customer"] != "cus_native" || in["attempt_id"] != in["key"] {
				t.Fatalf("recovery=%v", in)
			}
			body = `{"id":"cs_recovery","payment_intent":"pi_recovery","customer":"cus_native","url":"https://checkout.test/recovery"}`
		default:
			t.Fatalf("payment page %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	h := s.Handler()
	call := func(path, body string, status int) {
		t.Helper()
		r := httptest.NewRequest("POST", "https://controller.test/api/v1/team/billing/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", path)
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: session})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	created := time.Now().Unix()
	webhook := func(id, kind, object string) {
		t.Helper()
		now := time.Now().Unix()
		body := fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"created":%d,"livemode":false,"data":{"object":%s}}`, id, kind, created, object)
		mac := hmac.New(sha256.New, []byte("whsec_fixture"))
		if _, err := fmt.Fprintf(mac, "%d.%s", now, body); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/webhooks/stripe", strings.NewReader(body))
		r.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%x", now, mac.Sum(nil)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("webhook %s: %d %s", kind, w.Code, w.Body)
		}
	}
	for range 2 {
		call("card", "", 200)
	}
	for range 2 {
		webhook("evt_setup", "setup_intent.succeeded", `{"id":"seti_native","object":"setup_intent","livemode":false,"status":"succeeded","customer":"cus_native","payment_method":"pm_native"}`)
	}
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	spend, err := tenant.SpendStanding(t.Context(), time.Now())
	if err != nil || spend.Card.PaymentMethod != "pm_native" || spend.Billing.Tier != "New" {
		t.Fatalf("card=%+v: %v", spend, err)
	}
	for range 2 {
		call("checkout", `{"amount_cents":2000}`, 200)
	}
	for range 2 {
		webhook("evt_prepay", "checkout.session.completed", `{"id":"cs_prepay","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"pi_prepay","amount_total":2000,"currency":"usd"}`)
	}
	standing, err := tenant.BillingStanding(t.Context(), time.Now())
	if err != nil || standing.Tier != "Established" {
		t.Fatalf("prepay standing=%+v: %v", standing, err)
	}
	engineSQL(t, s.store, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,seconds,kind,amount_micro,charged_at) VALUES ('debt','a','','','',0,'storage',?,?)`, 2600*store.MicroCreditsPerCent, time.Now().UnixNano())
	s.cardBillingPass(t.Context())
	for range 2 {
		webhook("evt_decline", "payment_intent.payment_failed", `{"id":"pi_auto","object":"payment_intent","livemode":false,"status":"requires_payment_method","amount":600,"currency":"usd","customer":"cus_native","last_payment_error":{"decline_code":"insufficient_funds"}}`)
	}
	standing, err = tenant.BillingStanding(t.Context(), time.Now())
	if err != nil || standing.Tier != "Held" {
		t.Fatalf("decline standing=%+v: %v", standing, err)
	}
	for range 2 {
		call("pay", "", 200)
	}
	for range 2 {
		webhook("evt_recovery", "payment_intent.succeeded", `{"id":"pi_recovery","object":"payment_intent","livemode":false,"status":"succeeded","amount":600,"amount_received":600,"currency":"usd","customer":"cus_native","latest_charge":"ch_native"}`)
	}
	spend, err = tenant.SpendStanding(t.Context(), time.Now())
	if err != nil || spend.OpenCharge != nil || spend.BalanceMicro != 0 || spend.Billing.Tier != "Established" {
		t.Fatalf("recovered=%+v: %v", spend, err)
	}
	if counts["/internal/card-setup"] != 1 || counts["/internal/checkout"] != 1 || counts["/internal/charge-checkout"] != 1 {
		t.Fatalf("duplicate provider calls: %v", counts)
	}
}

func TestBillingModelProductionPageFailureKeepsReservation(t *testing.T) {
	for _, phase := range []string{"ambiguous", "revoked"} {
		t.Run(phase, func(t *testing.T) {
			s, session, csrf := billingNativeServer(t)
			calls := 0
			var key string
			s.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var in struct {
					Key string `json:"key"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Fatal(err)
				}
				if key != "" && key != in.Key {
					t.Fatal("provider key changed")
				}
				key = in.Key
				if phase == "ambiguous" && calls == 1 {
					return nil, errors.New("provider response lost")
				}
				if phase == "revoked" {
					engineSQL(t, s.store, `UPDATE memberships SET role='reader' WHERE team='a' AND account_id='owner'`)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"cs_retry","url":"https://checkout.test/page"}`))}, nil
			})
			h := s.Handler()
			call := func(want int) {
				t.Helper()
				r := httptest.NewRequest("POST", "https://controller.test/api/v1/team/billing/checkout", strings.NewReader(`{"amount_cents":2000}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Idempotency-Key", "retry")
				r.Header.Set("Origin", "https://controller.test")
				r.Header.Set(csrfHeaderName, csrf)
				r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: session})
				r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("response=%d %s", w.Code, w.Body)
				}
			}
			if phase == "ambiguous" {
				call(503)
				call(200)
				call(200)
				if calls != 2 {
					t.Fatalf("calls=%d", calls)
				}
			} else {
				call(403)
			}
			var n int
			if err := s.store.DB().QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE team='a'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("checkouts=%d: %v", n, err)
			}
			if phase == "revoked" {
				var data []byte
				if err := s.store.DB().QueryRow(`SELECT page_json FROM billing_pages WHERE team='a'`).Scan(&data); err != nil {
					t.Fatal(err)
				}
				var page capability.BillingPage
				if err := json.Unmarshal(data, &page); err != nil || page.URL != "" {
					t.Fatalf("stale authority completed page=%+v: %v", page, err)
				}
			}
		})
	}
}
