package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const checkoutToken = "checkout-secret"

type fakeCheckout struct {
	mu       sync.Mutex
	requests []checkoutCall
	status   int
	url      string
}

type checkoutCall struct {
	Bearer      string
	Key         string `json:"key"`
	Team        string `json:"team"`
	AmountCents int64  `json:"amount_cents"`
}

func newFakeCheckout(t *testing.T) (*fakeCheckout, string) {
	t.Helper()
	fc := &fakeCheckout{status: http.StatusOK, url: "https://checkout.stripe.test/c/pay/cs_test_1"}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/checkout" {
			http.NotFound(w, r)
			return
		}
		var call checkoutCall
		_ = json.NewDecoder(r.Body).Decode(&call)
		call.Bearer = r.Header.Get("Authorization")
		fc.mu.Lock()
		fc.requests = append(fc.requests, call)
		status, url := fc.status, fc.url
		fc.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cs_test_1", "url": url, "expires_at": time.Now().Add(31 * time.Minute).Unix(),
		})
	}))
	t.Cleanup(ts.Close)
	return fc, ts.URL
}

func (fc *fakeCheckout) calls() []checkoutCall {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]checkoutCall(nil), fc.requests...)
}

func billingFixture(t *testing.T) (*identityFixture, *fakeCheckout) {
	t.Helper()
	fc, url := newFakeCheckout(t)
	return newIdentityFixtureWith(t, fixtureOpts{
		checkoutURL: url, checkoutToken: checkoutToken,
	}), fc
}

type teamBilling struct {
	Team             string `json:"team"`
	BalanceMicro     int64  `json:"balance_micro"`
	MicroPerCredit   int64  `json:"micro_per_credit"`
	CreditsPerDollar int64  `json:"credits_per_dollar"`
	SliceSeconds     int64  `json:"slice_seconds"`
	PurchaseMinCents int64  `json:"purchase_min_cents"`
	RateTable        []struct {
		Cores          int64 `json:"cores"`
		MicroPerSecond int64 `json:"micro_per_second"`
	} `json:"rate_table"`
	Frozen          bool `json:"frozen"`
	Trusted         bool `json:"trusted"`
	CheckoutEnabled bool `json:"checkout_enabled"`
	CanPurchase     bool `json:"can_purchase"`
	Usage           []struct {
		RunID       string `json:"run_id"`
		AmountMicro int64  `json:"amount_micro"`
	} `json:"usage"`
	Grants []struct {
		Kind        string `json:"kind"`
		AmountMicro int64  `json:"amount_micro"`
		Reference   string `json:"reference"`
	} `json:"grants"`
}

func (f *identityFixture) trust(team string) {
	f.t.Helper()
	op := f.user("trust-op", "trust-operator@example.com")
	f.srv.WithOperatorAccounts([]string{op.id})
	if code := f.call("POST", "/api/v1/operator/teams/"+team+"/trust", op.auth, map[string]any{"trust": "granted", "reason": "test"}, nil); code != http.StatusOK {
		f.t.Fatalf("trust %s = %d", team, code)
	}
}

func TestTeamBilling_EveryMemberReadsTheActiveTeamsBilling(t *testing.T) {
	f, _ := billingFixture(t)
	owner, editor, reader := teamOf(f)
	outsider := f.user("x", "xena@example.com")
	reference := controller.PaidCreditsFixture(t, f.store, teamTenant(t, f.store, store.Team(owner.team)), 2_500)

	for _, tc := range []struct {
		name string
		who  signedIn
		buy  bool
	}{{"owner", owner, true}, {"editor", editor, false}, {"reader", reader, false}} {
		var b teamBilling
		if code := f.call("GET", "/api/v1/team/billing", tc.who.auth, nil, &b); code != http.StatusOK {
			t.Fatalf("%s read = %d", tc.name, code)
		}
		if b.Team != owner.team || b.BalanceMicro != 2_500*store.MicroCreditsPerCent {
			t.Errorf("%s reads team %s balance %d", tc.name, b.Team, b.BalanceMicro)
		}
		if b.CanPurchase != tc.buy || !b.CheckoutEnabled {
			t.Errorf("%s: can_purchase=%v checkout_enabled=%v, want %v and true", tc.name, b.CanPurchase, b.CheckoutEnabled, tc.buy)
		}
		if len(b.Grants) != 1 || b.Grants[0].Kind != store.CreditGrantPaid || b.Grants[0].Reference != reference {
			t.Errorf("%s grants = %+v", tc.name, b.Grants)
		}
		if b.MicroPerCredit != 100_000 || b.CreditsPerDollar != 1_000 || b.SliceSeconds != 60 ||
			b.PurchaseMinCents != 50 || b.Trusted || len(b.RateTable) == 0 {
			t.Errorf("%s reads prices %+v", tc.name, b)
		}
	}

	var other teamBilling
	if code := f.call("GET", "/api/v1/team/billing", outsider.auth, nil, &other); code != http.StatusOK {
		t.Fatalf("outsider read = %d", code)
	}
	if other.Team == owner.team || other.BalanceMicro != 0 || len(other.Grants) != 0 {
		t.Errorf("another team's member read %s's billing: %+v", owner.team, other)
	}
	if code := f.call("GET", "/api/v1/team/billing", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("an anonymous read = %d want 401", code)
	}
}

func TestTeamBillingCheckout_OpensASessionForTheOwnersActiveTeam(t *testing.T) {
	f, fc := billingFixture(t)
	if err := f.store.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	owner, editor, reader := teamOf(f)

	var out struct {
		URL string `json:"url"`
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_500}, &out); code != http.StatusOK {
		t.Fatalf("owner checkout = %d", code)
	}
	if out.URL != "https://checkout.stripe.test/c/pay/cs_test_1" {
		t.Errorf("url = %q", out.URL)
	}
	calls := fc.calls()
	if len(calls) != 1 || calls[0].Team != owner.team || calls[0].AmountCents != 2_500 ||
		calls[0].Bearer != "Bearer "+checkoutToken {
		t.Fatalf("checkout service saw %+v, want one call for %s, 2500 cents, with the token", calls, owner.team)
	}

	refused := []struct {
		name string
		who  signedIn
		body map[string]any
		want int
	}{
		{"an editor", editor, map[string]any{"amount_cents": 2_500}, http.StatusForbidden},
		{"a reader", reader, map[string]any{"amount_cents": 2_500}, http.StatusForbidden},
		{"a body naming a team", owner, map[string]any{"amount_cents": 2_500, "team": "someone-else"}, http.StatusBadRequest},
		{"a purchase below the provider minimum", owner, map[string]any{"amount_cents": 49}, http.StatusBadRequest},
		{"a negative purchase", owner, map[string]any{"amount_cents": -2_500}, http.StatusBadRequest},
	}
	for _, tc := range refused {
		if code := f.call("POST", "/api/v1/team/billing/checkout", tc.who.auth, tc.body, nil); code != tc.want {
			t.Errorf("%s = %d want %d", tc.name, code, tc.want)
		}
	}
	if n := len(fc.calls()); n != 1 {
		t.Errorf("refused checkouts reached the checkout service: %d calls", n)
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", "", map[string]any{"amount_cents": 2_500}, nil); code != http.StatusUnauthorized {
		t.Errorf("an anonymous checkout = %d want 401", code)
	}
}

func (f *identityFixture) checkoutRequest(auth string, cents int64, key string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodPost, f.url+"/api/v1/team/billing/checkout", strings.NewReader(fmt.Sprintf(`{"amount_cents":%d}`, cents)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", r.URL.Scheme+"://"+r.URL.Host)
	r.Header.Set("Idempotency-Key", key)
	if err := controller.ConsoleFixtureRequest(f.srv, r, sessionIDOf(auth)); err != nil {
		f.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, r)
	return w
}

func TestTeamBillingCheckout_ReportsAnUnusableCheckoutService(t *testing.T) {
	f, fc := billingFixture(t)
	if err := f.store.AsOperator().SetPaymentMode(t.Context(), capability.PaymentTest); err != nil {
		t.Fatal(err)
	}
	owner := f.user("o", "olga@example.com")
	f.trust(owner.team)
	for _, tc := range []struct {
		name   string
		status int
		url    string
	}{
		{"a failing service", http.StatusBadGateway, "https://checkout.stripe.test/x"},
		{"a non-https page", http.StatusOK, "http://checkout.stripe.test/x"},
	} {
		fc.mu.Lock()
		fc.status, fc.url = tc.status, tc.url
		fc.mu.Unlock()
		if w := f.checkoutRequest(owner.auth, 50_000, "retry-checkout"); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d %s, want 503", tc.name, w.Code, w.Body)
		}
	}
	fc.mu.Lock()
	fc.status, fc.url = http.StatusOK, "https://checkout.stripe.test/c/pay/cs_test_1"
	fc.mu.Unlock()
	if w := f.checkoutRequest(owner.auth, 50_000, "retry-checkout"); w.Code != http.StatusOK {
		t.Fatalf("retrying the reserved checkout = %d %s, want 200", w.Code, w.Body)
	}
	calls := fc.calls()
	if len(calls) != 3 || calls[0].Key == "" || calls[1].Key != calls[0].Key || calls[2].Key != calls[0].Key {
		t.Fatalf("provider retries changed reservation: %+v", calls)
	}
	var reservations int
	if err := teststore.SQL(t, f.store).QueryRow(`SELECT COUNT(*) FROM credit_checkouts WHERE team=?`, owner.team).Scan(&reservations); err != nil || reservations != 1 {
		t.Fatalf("checkout reservations = %d: %v, want one", reservations, err)
	}
	if balance, err := teamTenant(t, f.store, store.Team(owner.team)).CreditBalanceMicro(t.Context()); err != nil || balance != 0 {
		t.Fatalf("opening checkout changed the credit balance = %d: %v", balance, err)
	}

	plain := newIdentityFixture(t)
	solo := plain.user("o", "olga@example.com")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if code := plain.call("POST", "/api/v1/team/billing/checkout", solo.auth,
		map[string]any{"amount_cents": 2_500}, &body); code != http.StatusServiceUnavailable || body.Error.Code != "unavailable" || body.Error.Message != "request failed" {
		t.Errorf("checkout on a controller with no service = %d %q want 503 unavailable", code, body.Error.Code)
	}
	var b teamBilling
	plain.call("GET", "/api/v1/team/billing", solo.auth, nil, &b)
	if b.CheckoutEnabled || b.CanPurchase {
		t.Errorf("a controller with no checkout service offers purchases: %+v", b)
	}
}

func TestBillingTrust_OperatorGrantsAndRevokes(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	path := "/api/v1/operator/teams/" + owner.team + "/trust"
	if code := f.call("POST", path, owner.auth, map[string]any{"trust": "granted", "reason": "me"}, nil); code != http.StatusForbidden {
		t.Errorf("an owner granting itself trust = %d want 403", code)
	}
	if code := f.call("POST", path, operator.auth, map[string]any{"trust": "granted"}, nil); code != http.StatusBadRequest {
		t.Errorf("a grant with no reason = %d want 400", code)
	}
	if code := f.call("POST", "/api/v1/operator/teams/nobody/trust", operator.auth, map[string]any{"trust": "granted", "reason": "x"}, nil); code != http.StatusNotFound {
		t.Errorf("an unknown team's trust = %d want 404", code)
	}

	var got struct {
		Trust       string `json:"trust"`
		Trusted     bool   `json:"trusted"`
		TrustReason string `json:"trust_reason"`
		TrustBy     string `json:"trust_by"`
		Standing    string `json:"standing"`
	}
	if code := f.call("POST", path, operator.auth, map[string]any{"trust": "granted", "reason": "known customer"}, nil); code != http.StatusOK {
		t.Fatalf("grant = %d", code)
	}
	if code := f.call("POST", path, operator.auth, map[string]any{"trust": "revoked", "reason": "chargeback"}, &got); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if got.Trust != "revoked" || got.Trusted || got.TrustReason != "chargeback" || got.TrustBy != operator.id || got.Standing != "Held" {
		t.Errorf("after revoke = %+v", got)
	}
	var team operatorTeam
	if code := f.call("GET", "/api/v1/operator/teams/"+owner.team, operator.auth, nil, &team); code != http.StatusOK || team.Billing.Trust != "revoked" {
		t.Errorf("read after revoke = %d %+v", code, team.Billing)
	}
	if code := f.call("POST", path, operator.auth, map[string]any{"reason": "no trust named"}, nil); code != http.StatusBadRequest {
		t.Errorf("a change naming no trust = %d want 400", code)
	}
	if code := f.call("POST", path, operator.auth, map[string]any{"trust": "automatic", "reason": "cleared"}, &got); code != http.StatusOK ||
		got.Trust != "automatic" || got.Trusted || got.TrustReason != "cleared" || got.Standing != "New" {
		t.Errorf("reset = %d %+v, want the automatic rule", code, got)
	}
}
