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
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func billingNativeServer(t *testing.T) (*serverFixture, string, string) {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &serverFixture{}
	e, _ := engineRuntimeStore(t, fixture.option(deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) }))
	fixture.engine = e
	s := fixture
	engineSQL(t, s.store, `INSERT INTO accounts(id,email,email_verified,created_at,updated_at) VALUES ('owner','owner@example.test',1,1,1)`)
	engineSQL(t, s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	session, csrf, _, err := s.store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "owner"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a'`)
	if _, err := s.store.AsOperator().ConfigureDeployment(t.Context(), capability.PaymentTest, store.DefaultFreeTeamSlots, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	s.WithBillingCheckout("https://checkout.test", "credential")
	return s, session, csrf
}

func TestBillingModelProductionPages(t *testing.T) {
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
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"url":"https://checkout.stripe.com/page","id":"cs_native","expires_at":` + strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10) + `}`))}, nil
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
	call("POST", "checkout", `{"amount_cents":100001}`, 400)
	if calls != 1 {
		t.Fatalf("provider calls=%d", calls)
	}
}

func TestLargerPaymentRequestReachesOperatorQueueAtHighStanding(t *testing.T) {
	s, session, _ := billingNativeServer(t)
	s.WithOperatorAccounts([]string{"owner"})
	engineSQL(t, s.store, `UPDATE teams SET billing_trust='granted' WHERE name='a'`)
	s.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("oversized payment reached Checkout: %s", r.URL)
		return nil, nil
	})
	h := s.Handler()
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://controller.test/api/v1/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set("Idempotency-Key", path)
		if err := ConsoleFixtureRequest(s, r, session); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	for _, request := range []struct{ method, path, body string }{
		{"POST", "checkout", `{"amount_cents":100001}`},
		{"PUT", "refill", `{"enabled":true,"amount_cents":100001}`},
	} {
		refusal := call(request.method, "team/billing/"+request.path, request.body, http.StatusBadRequest)
		if !strings.Contains(refusal.Body.String(), "larger payments") || !strings.Contains(refusal.Body.String(), "request-tier") {
			t.Fatalf("%s refusal omitted the request flow: %s", request.path, refusal.Body)
		}
	}
	const message = "We need larger payments of $2,000 for release builds."
	request := call("POST", "team/billing/standing-request", `{"tier":"high","message":"`+message+`"}`, http.StatusCreated)
	var made controllerwire.StandingRequest
	if err := json.Unmarshal(request.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	listed := call("GET", "operator/standing-requests", "", http.StatusOK)
	var queue controllerwire.OperatorStandingQueue
	if err := json.Unmarshal(listed.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Requests) != 1 || queue.Requests[0].Request.ID != made.ID || queue.Requests[0].Request.Message != message || queue.Requests[0].Standing != "High" {
		t.Fatalf("larger-payment queue = %+v", queue)
	}
}

func TestBillingModelProductionCardPrepayAndRefill(t *testing.T) {
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
		case "/v1/charges/ch_auto":
			body = `{"id":"ch_auto","object":"charge","livemode":false,"payment_intent":"pi_auto","payment_method_details":{"card":{"fingerprint":"fp_native"}}}`
		case "/v1/charges/ch_other":
			body = `{"id":"ch_other","object":"charge","livemode":false,"payment_intent":"pi_other","payment_method_details":{"card":{"fingerprint":"fp_native"}}}`
		case "/v1/payment_intents/pi_prepay":
			body = `{"id":"pi_prepay","object":"payment_intent","livemode":false,"latest_charge":"ch_prepay"}`
		case "/v1/charges/ch_prepay":
			body = `{"id":"ch_prepay","object":"charge","livemode":false,"payment_intent":"pi_prepay","payment_method_details":{"card":{"fingerprint":"fp_native","three_d_secure":{"result":"authenticated"}}}}`
		default:
			t.Fatalf("provider lookup %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	s.WithPayments(verifier, s.store.AsOperator()).WithPaymentDeployment("Staging.Sparkwing.dev")
	counts := map[string]int{}
	var refillID string
	s.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		counts[r.URL.Path]++
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if in["team"] != "a" || in["deployment"] != "staging.sparkwing.dev" {
			t.Fatalf("provider team=%v", in)
		}
		body := ""
		switch r.URL.Path {
		case "/internal/card-setup":
			if in["setup_id"] != in["key"] || in["email"] != "owner@example.test" {
				t.Fatalf("setup=%v", in)
			}
			body = `{"id":"cs_setup","customer":"cus_native","url":"https://checkout.stripe.com/setup"}`
		case "/internal/portal":
			if len(in) != 3 || in["customer"] != "cus_native" {
				t.Fatalf("portal=%v", in)
			}
			body = `{"url":"https://billing.stripe.com/portal"}`
		case "/internal/checkout":
			body = `{"id":"cs_prepay","url":"https://checkout.stripe.com/prepay"}`
		case "/internal/refill":
			if _, method := in["payment_method"]; in["amount_cents"] != float64(1000) || in["customer"] != "cus_native" || method || in["receipt_email"] != nil {
				t.Fatalf("refill=%v", in)
			}
			refillID, _ = in["refill_id"].(string)
			body = `{"payment_intent":"pi_auto","status":"processing"}`
		default:
			t.Fatalf("payment page %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	h := s.Handler()
	call := func(path, body string, status int) {
		t.Helper()
		method := "POST"
		if path == "refill" {
			method = "PUT"
		}
		r := httptest.NewRequest(method, "https://controller.test/api/v1/team/billing/"+path, strings.NewReader(body))
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
	deliver := func(id, kind, object string) *httptest.ResponseRecorder {
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
		return w
	}
	webhook := func(id, kind, object string) {
		t.Helper()
		if w := deliver(id, kind, object); w.Code != 204 {
			t.Fatalf("webhook %s: %d %s", kind, w.Code, w.Body)
		}
	}
	const stamp = `"sparkwing_deployment":"staging.sparkwing.dev"`
	setupIntent := `{"id":"seti_native","object":"setup_intent","livemode":false,"metadata":{"purpose":"card_setup",` + stamp + `},"status":"succeeded","customer":"cus_native","payment_method":"pm_native"}`
	refilled := func(refill string) string {
		return `{"id":"pi_auto","object":"payment_intent","livemode":false,"metadata":{"purpose":"card_refill","refill_id":"` + refill + `",` + stamp + `},"status":"succeeded","amount":1000,"amount_received":1000,"currency":"usd","customer":"cus_native","latest_charge":"ch_auto"}`
	}
	for range 2 {
		call("card", "", 200)
	}
	var stored int
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM billing_pages WHERE CAST(page_json AS TEXT) LIKE '%owner@example.test%'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the receipt email reached the page record: %d, %v", stored, err)
	}
	webhook("evt_setup", "setup_intent.succeeded", setupIntent)
	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if spend, err := tenant.SpendStanding(t.Context()); err != nil || !spend.Card.AddedAt.IsZero() {
		t.Fatalf("setup attached a card before its page bound it: %+v: %v", spend.Card, err)
	}
	call("portal", "", 409)
	for range 2 {
		webhook("evt_setup_page", "checkout.session.completed", `{"id":"cs_setup","object":"checkout.session","mode":"setup","status":"complete","livemode":false,"customer":"cus_native","setup_intent":"seti_native","metadata":{"purpose":"card_setup",`+stamp+`}}`)
		webhook("evt_setup", "setup_intent.succeeded", setupIntent)
	}
	spend, err := tenant.SpendStanding(t.Context())
	if err != nil || spend.Card.Customer != "cus_native" || spend.Card.AddedAt.IsZero() || spend.Billing.Tier != "New" {
		t.Fatalf("card=%+v: %v", spend, err)
	}
	call("portal", "", 200)
	for range 2 {
		call("checkout", `{"amount_cents":2000}`, 200)
	}
	for range 2 {
		webhook("evt_prepay", "checkout.session.completed", `{"id":"cs_prepay","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"pi_prepay","amount_total":2000,"currency":"usd"}`)
	}
	standing, err := tenant.BillingStanding(t.Context())
	if err != nil || standing.Tier != "Established" {
		t.Fatalf("prepay standing=%+v: %v", standing, err)
	}
	call("refill", `{"enabled":true,"threshold_cents":500,"amount_cents":1000}`, 204)
	engineSQL(t, s.store, `INSERT INTO credit_charges(id,team,run_id,node_id,capability_prefix,seconds,kind,amount_micro,charged_at) VALUES ('spent','a','','','',0,'storage',?,?)`, 2000*store.MicroCreditsPerCent, time.Now().UnixNano())
	for range 2 {
		s.refillPass(t.Context())
	}
	if w := deliver("evt_unknown_refill", "payment_intent.succeeded", strings.NewReplacer("pi_auto", "pi_other", "ch_auto", "ch_other").Replace(refilled("refill_unknown"))); w.Code == 204 {
		t.Fatal("a payment naming no committed refill was acknowledged")
	}
	if spend, err := tenant.SpendStanding(t.Context()); err != nil || spend.BalanceMicro != 0 || !spend.Refill.Open {
		t.Fatalf("a payment for an unknown refill changed credit or closed the open refill: %+v: %v", spend, err)
	}
	for range 2 {
		webhook("evt_refill", "payment_intent.succeeded", refilled(refillID))
	}
	spend, err = tenant.SpendStanding(t.Context())
	if err != nil || spend.BalanceMicro != 1000*store.MicroCreditsPerCent || spend.Refill.Open || spend.Billing.Tier != "Established" {
		t.Fatalf("refilled=%+v: %v", spend, err)
	}
	if !spend.Billing.History.LastIncidentAt.IsZero() {
		t.Fatalf("a successful refill restarted clean days: %+v", spend.Billing.History)
	}
	if counts["/internal/card-setup"] != 1 || counts["/internal/checkout"] != 1 || counts["/internal/refill"] != 2 || counts["/internal/portal"] != 1 {
		t.Fatalf("provider calls: %v; want one page each, and one refill sent once per pass until its webhook", counts)
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
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"cs_retry","url":"https://checkout.stripe.com/page"}`))}, nil
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
			if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE team='a'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("checkouts=%d: %v", n, err)
			}
			if phase == "revoked" {
				var data []byte
				if err := teststore.SQL(t, s.store).QueryRow(`SELECT page_json FROM billing_pages WHERE team='a'`).Scan(&data); err != nil {
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

func TestCardBilling_OwnerSetsARefill(t *testing.T) {
	s, session, csrf := billingNativeServer(t)
	h := s.Handler()
	call := func(method, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://controller.test/api/v1/team/billing/refill", strings.NewReader(body))
		if method == "GET" {
			r.URL.Path = "/api/v1/team/billing"
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		r.Header.Set(csrfHeaderName, csrf)
		r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: session})
		r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s refill: %d %s, want %d", method, w.Code, w.Body, want)
		}
		return w
	}
	settings := func(team string) (enabled bool, threshold, amount int64) {
		t.Helper()
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT refill_enabled,refill_threshold_cents,refill_amount_cents FROM teams WHERE name=?`, team).Scan(&enabled, &threshold, &amount); err != nil {
			t.Fatal(err)
		}
		return enabled, threshold, amount
	}
	engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
	const on = `{"enabled":true,"threshold_cents":500,"amount_cents":2000,"week_limit_cents":6000,"month_limit_cents":0}`
	for _, role := range []string{"editor", "reader"} {
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE team='a' AND account_id='owner'`, role)
		call("PUT", on, 403)
	}
	engineSQL(t, s.store, `UPDATE memberships SET role='owner' WHERE team='a' AND account_id='owner'`)
	var refused struct {
		Error struct {
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(call("PUT", on, 409).Body.Bytes(), &refused); err != nil || refused.Error.Details.Reason != noCardCode {
		t.Fatalf("refill without a card: %+v: %v", refused, err)
	}
	if enabled, _, _ := settings("a"); enabled {
		t.Fatal("a refused refill turned on")
	}
	engineSQL(t, s.store, `UPDATE teams SET card_customer='cus',card_added_at=1 WHERE name='a'`)
	call("PUT", on, 204)
	var answer struct {
		Refill struct {
			Enabled         bool   `json:"enabled"`
			ThresholdCents  int64  `json:"threshold_cents"`
			AmountCents     int64  `json:"amount_cents"`
			WeekLimitCents  int64  `json:"week_limit_cents"`
			MonthLimitCents int64  `json:"month_limit_cents"`
			MinAmountCents  int64  `json:"min_amount_cents"`
			MaxAmountCents  int64  `json:"max_amount_cents"`
			Open            bool   `json:"open"`
			Limit           string `json:"limit"`
			Caps            struct {
				AmountCents int64 `json:"amount_cents"`
				PerDay      int64 `json:"per_day"`
				WeekCents   int64 `json:"week_cents"`
			} `json:"caps"`
		} `json:"refill"`
	}
	if err := json.Unmarshal(call("GET", "", 200).Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	r := answer.Refill
	if !r.Enabled || r.ThresholdCents != 500 || r.AmountCents != 2000 || r.WeekLimitCents != 6000 || r.MonthLimitCents != 0 ||
		r.MinAmountCents != store.MinRefillCents || r.MaxAmountCents != store.MaxCardPaymentCents || r.Open || r.Limit != "" ||
		r.Caps.AmountCents != 5000 || r.Caps.PerDay != 1 || r.Caps.WeekCents != 10000 {
		t.Fatalf("refill block=%+v", r)
	}
	for _, body := range []string{
		`{"enabled":true,"threshold_cents":500,"amount_cents":999}`,
		fmt.Sprintf(`{"enabled":true,"threshold_cents":500,"amount_cents":%d}`, store.MaxCardPaymentCents+1),
		`{"enabled":true,"threshold_cents":-1,"amount_cents":2000}`,
	} {
		call("PUT", body, 400)
	}
	if enabled, threshold, amount := settings("a"); !enabled || threshold != 500 || amount != 2000 {
		t.Fatalf("a refused refill changed the settings: %t %d %d", enabled, threshold, amount)
	}
	property := func(roleIndex uint8, threshold uint16) bool {
		role := []string{"owner", "editor", "reader"}[roleIndex%3]
		engineSQL(t, s.store, `UPDATE memberships SET role=? WHERE team='a' AND account_id='owner'`, role)
		_, before, _ := settings("a")
		wantStatus, wantThreshold := 403, before
		if role == "owner" {
			wantStatus, wantThreshold = 204, int64(threshold)
		}
		call("PUT", fmt.Sprintf(`{"enabled":true,"threshold_cents":%d,"amount_cents":2000}`, threshold), wantStatus)
		_, actual, _ := settings("a")
		foreignOn, foreign, _ := settings("foreign")
		return actual == wantThreshold && !foreignOn && foreign == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
}
