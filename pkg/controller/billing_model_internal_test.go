package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/capability/paymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type billingCloudProfile struct{}

func (billingCloudProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: 64}
	return match.Profile{Capacity: capacity}, capacity, nil
}

func TestBillingModelControllerNewCloudAwardCarriesLimits(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `UPDATE triggers SET expires_at=?`, time.Now().Add(24*time.Hour).UnixNano())
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp',card_added_at=? WHERE name='a'`, time.Now().UnixNano())
	engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
	execution := runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{Cores: runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: 4}}}}}}
	rawExecution, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "three"} {
		engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), rawExecution)
	}
	e.Store.SetClaimProfileResolver(billingCloudProfile{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := New(e.Store, nil).WithEngineController(e).Handler()
	for _, id := range []string{"one", "two", "three"} {
		status := http.StatusOK
		if id == "three" {
			status = http.StatusConflict
		}
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":%q,"available_cores":64}`, id), status)
		if id == "three" {
			continue
		}
		var response controllerwire.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Claim == nil || response.Claim.Binding.Deadline == nil || time.Until(*response.Claim.Binding.Deadline) > 4*time.Hour {
			t.Fatalf("wire award deadline=%+v", response.Claim)
		}
	}
}

func TestBillingModelControllerQueuePassesOverStandingLimitedTeams(t *testing.T) {
	for _, blocked := range []string{"capacity", "oversized"} {
		t.Run(blocked, func(t *testing.T) {
			e, _ := engineRuntimeStore(t)
			engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp' WHERE name='a'`)
			engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_payment_method,card_fingerprint) VALUES ('b',1,1,'customer-b','method-b','fp-b')`)
			engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
			engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('b','funded-b','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
			engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','later',number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at+1,log_retention_class,log_retention_days FROM runs WHERE id='run'`)
			engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT 'b','later',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
			for _, id := range []string{"one", "two", "three"} {
				engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":4}}}}`))
			}
			engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) SELECT 'b','later','job','pending',1,spec_json,execution_json FROM nodes WHERE team='a' AND node_id='one'`)
			rates := store.CreditRateTable{{Cores: 4, MicroPerSecond: 1}, {Cores: 16, MicroPerSecond: 4}}
			if _, err := e.Store.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{RateTable: &rates}); err != nil {
				t.Fatal(err)
			}
			e.Store.SetClaimProfileResolver(billingCloudProfile{})
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
			h := New(e.Store, nil).WithEngineController(e).Handler()
			if blocked == "capacity" {
				for _, id := range []string{"one", "two"} {
					engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":%q,"available_cores":64}`, id), http.StatusOK)
				}
			} else {
				engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE team='a'`, []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":16}}}}`))
			}
			w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":64}`, http.StatusOK)
			var response controllerwire.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Claim == nil || response.Claim.RunID != "later" {
				t.Fatalf("standing-limited team blocked eligible work: %s", w.Body)
			}
		})
	}
}

func TestBillingModelControllerPrepayAndChargeback(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp' WHERE name='a'`)
	for _, id := range []string{"one", "two"} {
		engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), []byte(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":16}}}}`))
	}
	rates := store.CreditRateTable{{Cores: 16, MicroPerSecond: 1}}
	if _, err := e.Store.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{RateTable: &rates}); err != nil {
		t.Fatal(err)
	}
	e.Store.SetClaimProfileResolver(billingCloudProfile{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h, webhook := billingModelPaymentHandler(t, e)
	request := `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"one","available_cores":64}`
	w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", request, http.StatusBadRequest)
	if !strings.Contains(w.Body.String(), "New") {
		t.Fatalf("oversized job omitted standing: %s", w.Body)
	}
	billingModelCheckout(t, e.Store, tenant, "prepay", 2000)
	webhook("prepay-paid", "checkout.session.completed", `{"id":"prepay","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"payment","amount_total":2000,"currency":"usd"}`)
	w = engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", request, http.StatusOK)
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim == nil {
		t.Fatalf("prepay did not admit job: %s: %v", w.Body, err)
	}
	w = engineClaimHTTP(t, h, response.Claim.Capability, "POST", "/api/v1/claims/exchange", `{}`, http.StatusOK)
	var exchanged controllerwire.ExchangeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &exchanged); err != nil {
		t.Fatal(err)
	}
	claim := exchanged.Claim
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/one/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
	object := `{"id":"dispute","object":"dispute","livemode":false,"status":"lost","currency":"usd","amount":2000,"payment_intent":"payment","charge":"charge","reason":"fraudulent"}`
	for _, id := range []string{"chargeback", "chargeback", "chargeback-replayed"} {
		webhook(id, "charge.dispute.closed", object)
	}
	standing, err := tenant.BillingStanding(t.Context(), time.Now())
	if err != nil || standing.Tier != "Held" {
		t.Fatalf("chargeback standing=%+v: %v", standing, err)
	}
	w = engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":64}`, http.StatusOK)
	var waiting controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &waiting); err != nil || waiting.Claim != nil {
		t.Fatalf("chargeback admitted work: %s: %v", w.Body, err)
	}
	w = engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/claims/"+claim.UnitID+"/heartbeat", `{"executing":true}`, http.StatusOK)
	var beat controllerwire.RenewalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &beat); err != nil || !beat.CancelRequested {
		t.Fatalf("chargeback hold left running work metering: %s: %v", w.Body, err)
	}
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/one/transition", `{"to":"success","cause":"report","source_attempt":1,"result":{"outcome":"success"}}`, http.StatusOK)
}

func refillFixture(t *testing.T) (*EngineController, *store.Tenant) {
	t.Helper()
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='cus_refill',card_payment_method='pm_refill',card_fingerprint='fp_refill',
	    refill_enabled=1,refill_threshold_cents=1000,refill_amount_cents=2000 WHERE name='a'`)
	return e, tenant
}

func refillIntent(id, status string) string {
	switch status {
	case "succeeded":
		return fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":false,"metadata":{"purpose":"card_refill"},"status":"succeeded","amount":2000,"amount_received":2000,"currency":"usd","customer":"cus_refill","latest_charge":"ch_%s"}`, id, strings.TrimPrefix(id, "pi_"))
	default:
		return fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":false,"metadata":{"purpose":"card_refill"},"status":"requires_payment_method","amount":2000,"currency":"usd","customer":"cus_refill","last_payment_error":{"decline_code":"insufficient_funds"}}`, id)
	}
}

// The worker opens a due refill, creates its payment unconfirmed, records the intent and only then confirms it;
// the confirmation's reply moves no money, and the signed success webhook grants the credit once.
func TestRefillPassCreatesRecordsThenConfirmsAndOnlyTheWebhookGrants(t *testing.T) {
	e, tenant := refillFixture(t)
	worker := New(e.Store, nil).WithBillingCheckout("https://checkout.test", "credential")
	var calls []string
	worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/internal/refill" {
			t.Fatalf("unexpected checkout call: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["team"] != "a" || body["amount_cents"] != float64(2000) || body["customer"] != "cus_refill" ||
			body["payment_method"] != "pm_refill" || body["refill_id"] == "" {
			t.Fatalf("refill request %v", body)
		}
		intent, _ := body["payment_intent"].(string)
		if len(calls) == 1 {
			var recorded string
			if err := e.Store.DB().QueryRow(`SELECT payment_id FROM credit_checkouts WHERE team='a' AND kind='refill'`).Scan(&recorded); err != nil || recorded != "pi_refill" {
				t.Fatalf("confirmed before the intent was recorded: %q %v", recorded, err)
			}
		}
		calls = append(calls, intent)
		status := "created"
		if intent != "" {
			status = "processing"
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"payment_intent":"pi_refill","status":%q}`, status)))}, nil
	})
	worker.refillPass(t.Context())
	if !slices.Equal(calls, []string{"", "pi_refill"}) {
		t.Fatalf("checkout calls %q; want creation then confirmation of the recorded intent", calls)
	}
	balance := func() int64 {
		t.Helper()
		micro, err := tenant.CreditBalanceMicro(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return micro
	}
	if balance() != 0 {
		t.Fatalf("the confirmation reply moved money: %d", balance())
	}
	standing, err := tenant.SpendStanding(t.Context(), time.Now())
	if err != nil || !standing.Refill.Open || standing.Refill.DayCount != 1 {
		t.Fatalf("refill state %+v: %v; want one open refill", standing.Refill, err)
	}
	provider := paymenttest.Provider{}.Charge("pi_refill", false, "", "fp_refill")
	_, deliver := billingModelProviderDelivery(t, e, provider, func(s *Server) *Server { return s })
	for _, id := range []string{"evt_refill", "evt_refill"} {
		if w := deliver(id, "payment_intent.succeeded", refillIntent("pi_refill", "succeeded")); w.Code != http.StatusNoContent {
			t.Fatalf("refill webhook = %d %s", w.Code, w.Body)
		}
	}
	if balance() != 2000*store.MicroCreditsPerCent {
		t.Fatalf("balance after the refill = %d; want $20 granted once", balance())
	}
	standing, err = tenant.SpendStanding(t.Context(), time.Now())
	if err != nil || standing.Refill.Open || standing.Refill.DayCount != 1 || standing.Refill.WeekCents != 2000 {
		t.Fatalf("refill state %+v: %v; want it settled and counted", standing.Refill, err)
	}
	calls = nil
	worker.refillPass(t.Context())
	if len(calls) != 0 {
		t.Fatalf("a settled refill was charged again: %q", calls)
	}
}

// A signed failure closes the refill, stops refills until the card or settings change, and alerts and mails the owners.
func TestARefusedRefillStopsRefillsAndAlerts(t *testing.T) {
	e, tenant := refillFixture(t)
	due, err := e.Store.AsOperator().DueRefills(t.Context())
	if err != nil || len(due) != 1 || due[0].AmountCents != 2000 || due[0].PaymentIntent != "" {
		t.Fatalf("due refills %+v: %v", due, err)
	}
	if err := e.Store.AsOperator().RecordRefillIntent(t.Context(), "a", due[0].ID, "pi_declined"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, deliver := billingModelProviderDelivery(t, e, paymenttest.Provider{}, func(s *Server) *Server {
		s.logger = slog.New(slog.NewJSONHandler(&logs, nil))
		return s
	})
	if w := deliver("evt_declined", "payment_intent.payment_failed", refillIntent("pi_declined", "failed")); w.Code != http.StatusNoContent {
		t.Fatalf("failure webhook = %d %s", w.Code, w.Body)
	}
	standing, err := tenant.SpendStanding(t.Context(), time.Now())
	if err != nil || standing.Refill.Open || standing.Refill.FailedAt.IsZero() || standing.Refill.Failure != "insufficient_funds" {
		t.Fatalf("refill state %+v: %v; want the refill closed and its failure recorded", standing.Refill, err)
	}
	if micro, err := tenant.CreditBalanceMicro(t.Context()); err != nil || micro != 0 {
		t.Fatalf("a refused refill granted %d: %v", micro, err)
	}
	if !strings.Contains(logs.String(), `"alert":"refill_failed"`) || !strings.Contains(logs.String(), `"decline_code":"insufficient_funds"`) {
		t.Fatalf("a refused refill raised no alert: %s", logs.String())
	}
	if due, err := e.Store.AsOperator().DueRefills(t.Context()); err != nil || len(due) != 0 {
		t.Fatalf("refills after a failure %+v: %v; want none until the card or settings change", due, err)
	}
}

func billingModelPaymentHandler(t *testing.T, e *EngineController) (http.Handler, func(string, string, string)) {
	t.Helper()
	h, deliver := billingModelPaymentDelivery(t, e, func(s *Server) *Server { return s })
	return h, func(id, kind, object string) {
		t.Helper()
		if w := deliver(id, kind, object); w.Code != http.StatusNoContent {
			t.Fatalf("payment=%d %s", w.Code, w.Body)
		}
	}
}

func billingModelPaymentDelivery(t *testing.T, e *EngineController, configure func(*Server) *Server) (http.Handler, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	provider := paymenttest.Provider{}.SettledCheckout("pi_fixture", false, "authenticated").SettledCheckout("payment", false, "authenticated")
	return billingModelProviderDelivery(t, e, provider, configure)
}

func billingModelProviderDelivery(t *testing.T, e *EngineController, provider paymenttest.Provider, configure func(*Server) *Server) (http.Handler, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	key, secret := filepath.Join(t.TempDir(), "api-key"), filepath.Join(t.TempDir(), "webhook-secret")
	for path, value := range map[string]string{key: "sk_test_fixture", secret: "whsec_fixture"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := capability.NewPaymentAuthenticator(capability.PaymentConfig{Mode: capability.PaymentTest, APIKeyFile: key, WebhookSecretFile: secret, HTTPTransport: provider})
	if err != nil {
		t.Fatal(err)
	}
	h := configure(New(e.Store, nil).WithEngineController(e).WithPayments(verifier, e.Store)).Handler()
	now := time.Now().Unix()
	deliver := func(id, kind, object string) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"created":%d,"livemode":false,"data":{"object":%s}}`, id, kind, now, object)
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
	return h, deliver
}

type billingIdentityProvider struct{}

func (billingIdentityProvider) AuthorizeURL(string, string, string) string {
	return "https://identity.test/authorize"
}

func (billingIdentityProvider) SignIn(_ context.Context, code, _, _ string) (store.SignInProfile, error) {
	return store.SignInProfile{Provider: store.ProviderGoogle, Subject: code, Email: code + "@example.test", EmailVerified: true}, nil
}

func TestBillingModelControllerWaitlistsThe101stAccount(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	if _, err := e.Store.SetSignUpLimits(t.Context(), store.SignUpLimits{}); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := New(e.Store, nil).WithSecretsCipher(cipher).WithLicense(accountSessionTestLicense(t)).withProvider(store.ProviderGoogle, billingIdentityProvider{}, []string{"https://app.test/callback"})
	for i := range 101 {
		_, _, err := s.oauthSignIn(t.Context(), store.ProviderGoogle, fmt.Sprint("signup", i), "verifier", "https://app.test/callback")
		if err != nil {
			t.Fatal(err)
		}
		account, err := e.Store.AccountByEmail(t.Context(), fmt.Sprint("signup", i, "@example.test"))
		if err != nil {
			t.Fatal(err)
		}
		if account.Waitlisted != (i == 100) {
			t.Fatalf("signup %d: %+v", i+1, account)
		}
		if i == 100 && account.ActiveTeam != "" {
			t.Fatalf("waitlisted account reached a team: %+v", account)
		}
	}
	status, err := s.signUpStatus(t.Context(), time.Now())
	if err != nil || status.State != "waitlist" || !slices.Contains(status.Reasons, "launch_capacity") {
		t.Fatalf("launch cap is invisible to operator: %+v: %v", status, err)
	}
}

func TestBillingModelControllerReportsStandingAndKeepsCardPrepay(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	terms := licensetest.Terms{Features: []string{license.FeatureMultiTeam, license.FeatureMetering}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	server := New(e.Store, nil).WithSecretsCipher(cipher).WithLicense(license.Resolve(licensetest.Sign(t, priv, terms), pub, now, nil)).WithBillingCheckout("https://checkout.test", "credential")
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('owner','owner@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_fingerprint='fp' WHERE name='a'`)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 600000 * store.MicroCreditsPerCent, Reference: "console:grant", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := e.Store.CreateAccountSession(t.Context(), store.Account{ID: "owner"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "https://controller.test/api/v1/team/billing", nil)
	request.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, server.cookiesSecure()), Value: raw})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("billing read=%d %s", response.Code, response.Body)
	}
	var body struct {
		CanPurchase bool           `json:"can_purchase"`
		Limits      map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.CanPurchase {
		t.Fatalf("card blocked prepay: %s", response.Body)
	}
	limits := body.Limits
	if limits["standing"] != "Established" || limits["cloud_vcpu"] != float64(32) || limits["max_job_seconds"] != float64(14400) {
		t.Fatalf("standing=%+v", limits)
	}
	for _, retired := range []string{"ceiling_cents", "daily_cap_cents", "credit_limit_cents", "budget_cents", "rung_cents", "headroom_cents"} {
		if _, exists := limits[retired]; exists {
			t.Fatalf("retired billing field %s", retired)
		}
	}
}

func TestPaymentOwnershipFollowsTheStoredIdentityNotTheHost(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	id, err := e.Store.AsOperator().DeploymentID(t.Context())
	if err != nil || !strings.HasPrefix(id, "dep-") {
		t.Fatalf("deployment id %q: %v", id, err)
	}
	if again, err := e.Store.AsOperator().DeploymentID(t.Context()); err != nil || again != id {
		t.Fatalf("deployment id changed: %q then %q: %v", id, again, err)
	}
	billingModelCheckout(t, e.Store, tenant, "cs_owned", 2000)
	session := func(stamp string) string {
		return `{"id":"cs_owned","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"pi_fixture","amount_total":2000,"currency":"usd","metadata":{"sparkwing_deployment":"` + stamp + `"}}`
	}
	grants := func() int {
		var n int
		if err := e.Store.DB().QueryRow(`SELECT COUNT(*) FROM credit_grants WHERE kind='paid'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for i, host := range []string{"https://staging.sparkwing.dev", "https://renamed.example"} {
		_, deliver := billingModelPaymentDelivery(t, e, func(s *Server) *Server { return s.WithExternalURL(host).WithPaymentDeployment(id) })
		if w := deliver(fmt.Sprintf("evt_owned_%d", i), "checkout.session.completed", session(id)); w.Code != http.StatusNoContent || grants() != 1 {
			t.Fatalf("%s: our own payment = %d %s, grants %d", host, w.Code, w.Body, grants())
		}
		if w := deliver(fmt.Sprintf("evt_conflict_%d", i), "checkout.session.completed", session("dep-another")); w.Code < 500 || grants() != 1 {
			t.Fatalf("%s: recorded payment stamped elsewhere = %d, grants %d; want a retryable refusal and no settlement", host, w.Code, grants())
		}
	}
}
