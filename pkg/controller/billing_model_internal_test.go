package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method',card_added_at=? WHERE name='a'`, time.Now().UnixNano())
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
	engineSQL(t, e.Store, `UPDATE teams SET card_customer='customer',card_payment_method='method' WHERE name='a'`)
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
		CardBilled  bool           `json:"card_billed"`
		CanPurchase bool           `json:"can_purchase"`
		Limits      map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.CardBilled || !body.CanPurchase {
		t.Fatalf("card blocked prepay: %s", response.Body)
	}
	limits := body.Limits
	if limits["standing"] != "High" || limits["cloud_vcpu"] != float64(512) || limits["max_job_seconds"] != float64(21600) {
		t.Fatalf("standing=%+v", limits)
	}
	for _, retired := range []string{"ceiling_cents", "daily_cap_cents", "credit_limit_cents"} {
		if _, exists := limits[retired]; exists {
			t.Fatalf("retired billing field %s", retired)
		}
	}
}
