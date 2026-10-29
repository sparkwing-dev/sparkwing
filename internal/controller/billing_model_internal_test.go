package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type billingCloudProfile struct{}

func (billingCloudProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, error) {
	return match.Profile{Labels: []string{"sparkwing-4cpu"}}, nil
}

func TestBillingModelControllerNewCloudAwardCarriesLimits(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	engineSQL(t, e.Store, `UPDATE triggers SET expires_at=?`, time.Now().Add(24*time.Hour).UnixNano())
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_added_at=1,card_added_at=? WHERE name='a'`, time.Now().UnixNano())
	engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
	execution := runspec.JobExecutionSettings{Requires: runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"sparkwing-4cpu"}}}}
	rawExecution, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "three"} {
		engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), rawExecution)
	}
	engineAcceptedFixturePlan(t, e, "a", "run", "one", "two", "three")
	e.claimProfiles = billingCloudProfile{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithEngineController(e.EngineController).Handler()
	for _, id := range []string{"one", "two", "three"} {
		status := http.StatusOK
		if id == "three" {
			status = http.StatusConflict
		}
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":%q}`, id), status)
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
			engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_added_at=1 WHERE name='a'`)
			engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at,card_customer,card_added_at) VALUES ('b',1,1,'customer-b',1)`)
			engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('a','funded-a','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
			engineSQL(t, e.Store, `INSERT INTO credit_grants(team,id,kind,amount_micro,reference,created_by,created_at) VALUES ('b','funded-b','free',?,'test','operator',1)`, 1_500*store.MicroCreditsPerCent)
			for _, id := range []string{"one", "two", "three"} {
				engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), []byte(`{"requires":{"set":true,"value":["sparkwing-4cpu"]}}`))
			}
			engineAcceptedFixturePlan(t, e, "a", "run", "one", "two", "three")
			engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash) SELECT 'b','later',number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at+1,log_retention_class,log_retention_days,spec_json,spec_hash,sealed_at,plan_json,plan_hash FROM runs WHERE id='run'`)
			engineSQL(t, e.Store, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels) SELECT 'b','later',unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels FROM triggers WHERE id='run'`)
			engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,queue_started_at,spec_json,execution_json) SELECT 'b','later','job','pending',1,queue_started_at,?,execution_json FROM nodes WHERE team='a' AND node_id='one'`, []byte(`{"id":"job"}`))
			engineAcceptedFixturePlan(t, e, "b", "later", "job")
			rates := store.CreditRateTable{{Cores: 4, MicroPerSecond: 1}, {Cores: 16, MicroPerSecond: 4}}
			teststore.SetCreditSettings(t, e.Store, store.CreditSettingsUpdate{RateTable: &rates})
			e.claimProfiles = billingCloudProfile{}
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
			h := NewDeployment(e.Store, deploymenttest.Team(t), nil).WithEngineController(e.EngineController).Handler()
			if blocked == "capacity" {
				for _, id := range []string{"one", "two"} {
					engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":%q}`, id), http.StatusOK)
				}
			} else {
				engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE team='a'`, []byte(`{"requires":{"set":true,"value":["sparkwing-16cpu"]}}`))
			}
			w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
			var response controllerwire.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Claim == nil || response.Claim.RunID != "later" || response.Claim.JobID != "job" {
				t.Fatalf("standing-limited team blocked eligible work: %s", w.Body)
			}
			claim := response.Claim
			var plan nodeproto.PlanDoc
			if claim.Request.RunID != "later" || claim.Request.Request.Team != "b" || claim.Accepted == nil ||
				claim.Accepted.Spec.Request.RunID != "later" || claim.Accepted.Spec.Request.Request.Team != "b" ||
				json.Unmarshal(claim.Accepted.Plan, &plan) != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "job" {
				t.Fatalf("award carries another run's execution inputs: %s", w.Body)
			}
		})
	}
}

func TestBillingModelControllerPrepayAndChargeback(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_added_at=1 WHERE name='a'`)
	for _, id := range []string{"one", "two"} {
		engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, id, []byte(fmt.Sprintf(`{"id":%q}`, id)), []byte(`{"requires":{"set":true,"value":["sparkwing-16cpu"]}}`))
	}
	engineAcceptedFixturePlan(t, e, "a", "run", "one", "two")
	rates := store.CreditRateTable{{Cores: 16, MicroPerSecond: 1}}
	teststore.SetCreditSettings(t, e.Store, store.CreditSettingsUpdate{RateTable: &rates})
	e.claimProfiles = billingCloudProfile{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h, webhook := billingModelPaymentHandler(t, e)
	request := `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"one"}`
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
	engineAdmitBearer(t, e.Store, claim.Capability)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/one/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
	object := `{"id":"dispute","object":"dispute","livemode":false,"status":"lost","currency":"usd","amount":2000,"payment_intent":"payment","charge":"charge","reason":"fraudulent"}`
	for _, id := range []string{"chargeback", "chargeback", "chargeback-replayed"} {
		webhook(id, "charge.dispute.closed", object)
	}
	standing, err := tenant.BillingStanding(t.Context())
	if err != nil || standing.Tier != "Held" {
		t.Fatalf("chargeback standing=%+v: %v", standing, err)
	}
	w = engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
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

func refillFixture(t *testing.T) (*engineFixture, *store.Tenant) {
	t.Helper()
	e, tenant := engineRuntimeStore(t)
	engineSQL(t, e.Store, `INSERT INTO settings(key,value) VALUES ('payment_mode',?)`, []byte(`"test"`))
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='cus_refill',card_added_at=1,
	    refill_enabled=1,refill_threshold_cents=1000,refill_amount_cents=2000 WHERE name='a'`)
	return e, tenant
}

// refillIntent is a refill payment intent whose metadata names refill, as the checkout service creates it.
func refillIntent(id, refill, status string) string {
	meta := fmt.Sprintf(`"metadata":{"purpose":"card_refill","refill_id":%q}`, refill)
	switch status {
	case "requires_action":
		return fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":false,%s,"status":"requires_action","amount":2000,"currency":"usd","customer":"cus_refill"}`, id, meta)
	case "succeeded":
		return fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":false,%s,"status":"succeeded","amount":2000,"amount_received":2000,"currency":"usd","customer":"cus_refill","latest_charge":"ch_%s"}`, id, meta, strings.TrimPrefix(id, "pi_"))
	default:
		return fmt.Sprintf(`{"id":%q,"object":"payment_intent","livemode":false,%s,"status":"requires_payment_method","amount":2000,"currency":"usd","customer":"cus_refill","last_payment_error":{"decline_code":"insufficient_funds"}}`, id, meta)
	}
}

func TestUnmeteredControllerDoesNotRefill(t *testing.T) {
	e, _ := refillFixture(t)
	worker := New(e.Store, nil).WithBillingCheckout("https://checkout.test", "credential")
	worker.checkout.http.Transport = billingPageTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unmetered controller called the payment service: %s", r.URL.Path)
		return nil, nil
	})
	worker.refillPass(t.Context())
	var opened int
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE kind='refill'`).Scan(&opened); err != nil || opened != 0 {
		t.Fatalf("unmetered controller opened %d refills: %v", opened, err)
	}
}

func billingModelPaymentHandler(t *testing.T, e *engineFixture) (http.Handler, func(string, string, string)) {
	t.Helper()
	h, deliver := billingModelPaymentDelivery(t, e, func(s *Server) *Server { return s })
	return h, func(id, kind, object string) {
		t.Helper()
		if w := deliver(id, kind, object); w.Code != http.StatusNoContent {
			t.Fatalf("payment=%d %s", w.Code, w.Body)
		}
	}
}

func billingModelPaymentDelivery(t *testing.T, e *engineFixture, configure func(*Server) *Server) (http.Handler, func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	provider := paymenttest.Provider{}.SettledCheckout("pi_fixture", false, "authenticated").SettledCheckout("payment", false, "authenticated")
	return billingModelProviderDelivery(t, e, provider, configure)
}

func billingModelProviderDelivery(t *testing.T, e *engineFixture, provider paymenttest.Provider, configure func(*Server) *Server) (http.Handler, func(string, string, string) *httptest.ResponseRecorder) {
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
	h := configure(NewDeployment(e.Store, deploymenttest.Team(t), nil).WithEngineController(e.EngineController).WithPayments(verifier, e.Store.AsOperator())).Handler()
	now := time.Now().Unix()
	deliver := func(id, kind, object string) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"created":%d,"livemode":false,"data":{"object":%s}}`, id, kind, now, object)
		mac := hmac.New(sha256.New, []byte("whsec_fixture"))
		if _, err := fmt.Fprintf(mac, "%d.%s", now, body); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(body))
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
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	zero := 0
	if err := e.Store.AsOperator().WriteSignUpSettings(t.Context(), teststore.OperatorAuthorized(t, e.Store, ""), store.SignUpSettingsChange{HourlyLimit: &zero, DailyLimit: &zero, HourlyWarn: &zero, GitHubMinAccountDays: &zero, FreeTeamMembers: &zero}); err != nil {
		t.Fatal(err)
	}
	s := e.Server.withProvider(store.ProviderGoogle, billingIdentityProvider{}, []string{"https://app.test/callback"})
	for i := range 101 {
		_, _, err := s.oauthSignIn(t.Context(), store.ProviderGoogle, fmt.Sprint("signup", i), "verifier", "https://app.test/callback")
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := teststore.SQL(t, e.Store).QueryRowContext(t.Context(), `SELECT id FROM accounts WHERE email=?`, fmt.Sprint("signup", i, "@example.test")).Scan(&id); err != nil {
			t.Fatal(err)
		}
		account, err := e.Store.AsOperator().Account(t.Context(), id)
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
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, tenant := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	server := e.Server.WithBillingCheckout("https://checkout.test", "credential")
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('owner','owner@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','owner','owner',1)`)
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_added_at=1 WHERE name='a'`)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 600000 * store.MicroCreditsPerCent, Reference: "console:grant", CreatedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	raw, _, _, err := e.Store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "owner"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://controller.test/api/v1/team/billing", nil)
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
	id, err := e.Store.AsOperator().ConfigureDeployment(t.Context(), "", func() error { return nil })
	if err != nil || !strings.HasPrefix(id, "dep-") {
		t.Fatalf("deployment id %q: %v", id, err)
	}
	if again, err := e.Store.AsOperator().ConfigureDeployment(t.Context(), "", func() error { return nil }); err != nil || again != id {
		t.Fatalf("deployment id changed: %q then %q: %v", id, again, err)
	}
	billingModelCheckout(t, e.Store, tenant, "cs_owned", 2000)
	session := func(stamp string) string {
		return `{"id":"cs_owned","object":"checkout.session","mode":"payment","livemode":false,"payment_status":"paid","payment_intent":"pi_fixture","amount_total":2000,"currency":"usd","metadata":{"sparkwing_deployment":"` + stamp + `"}}`
	}
	grants := func() int {
		var n int
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM credit_grants WHERE kind='paid'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for i, host := range []string{"https://staging.sparkwing.dev", "https://renamed.example"} {
		_, deliver := billingModelPaymentDelivery(t, e, func(s *Server) *Server {
			s.deployment = deploymenttest.Cloud(t, host)
			return s.WithPaymentDeployment(id)
		})
		if w := deliver(fmt.Sprintf("evt_owned_%d", i), "checkout.session.completed", session(id)); w.Code != http.StatusNoContent || grants() != 1 {
			t.Fatalf("%s: our own payment = %d %s, grants %d", host, w.Code, w.Body, grants())
		}
		if w := deliver(fmt.Sprintf("evt_conflict_%d", i), "checkout.session.completed", session("dep-another")); w.Code < 500 || grants() != 1 {
			t.Fatalf("%s: recorded payment stamped elsewhere = %d, grants %d; want a retryable refusal and no settlement", host, w.Code, grants())
		}
	}
}

type refillMail struct{ sent []mailer.Message }

func (m *refillMail) Send(_ context.Context, msg mailer.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}
