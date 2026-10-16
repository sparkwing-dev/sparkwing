package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestBillingModelProductionPagesAndBudget(t *testing.T) {
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
	engineSQL(t, s.store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	s.WithLicense(license.Resolve(licensetest.Sign(t, priv, licensetest.Terms{Features: []string{license.FeatureMultiTeam, license.FeatureMetering}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}), pub, now, nil)).WithBillingCheckout("https://checkout.test", "credential")
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
		r.Header.Set("Idempotency-Key", "native")
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
