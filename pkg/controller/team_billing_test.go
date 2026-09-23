package controller_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const checkoutToken = "checkout-secret"

// fakeCheckout stands in for the hosted checkout service: it records what
// the controller asked for and answers with a Stripe-shaped page, or with
// whatever status and url a test sets.
type fakeCheckout struct {
	mu       sync.Mutex
	requests []checkoutCall
	status   int
	url      string
}

type checkoutCall struct {
	Bearer      string
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
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "cs_test_1", "url": url})
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
	raw, pub := multiTeamLicense(t)
	return newIdentityFixtureWith(t, fixtureOpts{
		license: raw, key: pub, checkoutURL: url, checkoutToken: checkoutToken,
	}), fc
}

type teamBilling struct {
	Team               string `json:"team"`
	BalanceMicro       int64  `json:"balance_micro"`
	BalanceCapMicro    int64  `json:"balance_cap_micro"`
	MicroPerCredit     int64  `json:"micro_per_credit"`
	CreditsPerDollar   int64  `json:"credits_per_dollar"`
	MinBillableSeconds int64  `json:"min_billable_seconds"`
	PurchaseMinCents   int64  `json:"purchase_min_cents"`
	PurchaseMaxCents   int64  `json:"purchase_max_cents"`
	RateTable          []struct {
		Cores          int64 `json:"cores"`
		MicroPerSecond int64 `json:"micro_per_second"`
	} `json:"rate_table"`
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

type capRefusal struct {
	Code         string `json:"code"`
	Error        string `json:"error"`
	BalanceMicro int64  `json:"balance_micro"`
	CapMicro     int64  `json:"cap_micro"`
	AmountMicro  int64  `json:"amount_micro"`
}

func (f *identityFixture) grantTo(team, kind, reference string, micro int64) int {
	f.t.Helper()
	return f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": kind, "amount_micro": micro, "reference": reference, "team": team,
	}, nil)
}

func TestTeamBilling_EveryMemberReadsTheActiveTeamsBilling(t *testing.T) {
	f, _ := billingFixture(t)
	owner, editor, reader := teamOf(f)
	outsider := f.user("x", "xena@example.com")
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", 2_500*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}

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
		if len(b.Grants) != 1 || b.Grants[0].Kind != store.CreditGrantPaid || b.Grants[0].Reference != "pi_1" {
			t.Errorf("%s grants = %+v", tc.name, b.Grants)
		}
		if b.MicroPerCredit != 5_000 || b.CreditsPerDollar != 20_000 || b.MinBillableSeconds != 20 ||
			b.PurchaseMinCents != 500 || b.PurchaseMaxCents != 50_000 ||
			b.BalanceCapMicro != 5_000*100*store.MicroCreditsPerCent || len(b.RateTable) != 3 {
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
		{"a purchase under $5", owner, map[string]any{"amount_cents": 499}, http.StatusBadRequest},
		{"a purchase over $500", owner, map[string]any{"amount_cents": 50_001}, http.StatusBadRequest},
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

func TestTeamBillingCheckout_RefusesAPurchaseAboveTheBalanceCap(t *testing.T) {
	f, fc := billingFixture(t)
	owner := f.user("o", "olga@example.com")
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_big", 4_990*100*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	var refusal capRefusal
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_500}, &refusal); code != http.StatusConflict {
		t.Fatalf("a checkout past the cap = %d want 409", code)
	}
	if refusal.Code != "balance_cap" || refusal.BalanceMicro != 4_990*100*store.MicroCreditsPerCent ||
		refusal.CapMicro != 5_000*100*store.MicroCreditsPerCent || refusal.AmountMicro != 2_500*store.MicroCreditsPerCent {
		t.Errorf("refusal = %+v", refusal)
	}
	if len(fc.calls()) != 0 {
		t.Error("a checkout refused at the cap still opened a session")
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 1_000}, nil); code != http.StatusOK {
		t.Errorf("a checkout landing exactly on the cap = %d want 200", code)
	}
}

func TestTeamBillingCheckout_ReportsAnUnusableCheckoutService(t *testing.T) {
	f, fc := billingFixture(t)
	owner := f.user("o", "olga@example.com")
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
		if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
			map[string]any{"amount_cents": 2_500}, nil); code != http.StatusBadGateway {
			t.Errorf("%s = %d want 502", tc.name, code)
		}
	}

	plain := newIdentityFixture(t)
	solo := plain.user("o", "olga@example.com")
	var body struct {
		Code string `json:"code"`
	}
	if code := plain.call("POST", "/api/v1/team/billing/checkout", solo.auth,
		map[string]any{"amount_cents": 2_500}, &body); code != http.StatusServiceUnavailable || body.Code != "checkout_unavailable" {
		t.Errorf("checkout on a controller with no service = %d %q want 503 checkout_unavailable", code, body.Code)
	}
	var b teamBilling
	plain.call("GET", "/api/v1/team/billing", solo.auth, nil, &b)
	if b.CheckoutEnabled || b.CanPurchase {
		t.Errorf("a controller with no checkout service offers purchases: %+v", b)
	}
}

func TestCreditsGrant_RefusesAGrantAboveTheBalanceCap(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	capMicro := int64(5_000 * 100 * store.MicroCreditsPerCent)
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", capMicro-store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("a grant one cent under the cap = %d", code)
	}
	var refusal capRefusal
	if code := f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": "paid", "amount_micro": 2 * store.MicroCreditsPerCent, "reference": "pi_2", "team": owner.team,
	}, &refusal); code != http.StatusConflict || refusal.Code != "balance_cap" {
		t.Fatalf("a grant past the cap = %d %+v want 409 balance_cap", code, refusal)
	}
	if code := f.grantTo(owner.team, store.CreditGrantFree, "gift", 2*store.MicroCreditsPerCent); code != http.StatusConflict {
		t.Errorf("a free grant past the cap = %d want 409", code)
	}
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_3", store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Errorf("a grant landing exactly on the cap = %d want 201", code)
	}
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", capMicro-store.MicroCreditsPerCent); code != http.StatusOK {
		t.Errorf("a replayed payment at the cap = %d want 200: a webhook retry must still succeed", code)
	}
	if code := f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": "reversal", "amount_micro": -store.MicroCreditsPerCent, "reference": "re_1", "reverses": "pi_3",
	}, nil); code != http.StatusCreated {
		t.Errorf("a reversal at the cap = %d want 201", code)
	}
}

func TestCreditsGrant_AReversalFindsTheTeamThatPaid(t *testing.T) {
	f := newIdentityFixture(t)
	payer := f.user("o", "olga@example.com")
	other := f.user("x", "xena@example.com")
	if code := f.grantTo(payer.team, store.CreditGrantPaid, "pi_1", 1_000*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	reverse := func(reverses string) int {
		return f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
			"kind": "reversal", "amount_micro": -400 * store.MicroCreditsPerCent,
			"reference": "re_" + reverses, "reverses": reverses,
		}, nil)
	}
	if code := reverse("pi_1"); code != http.StatusCreated {
		t.Fatalf("a reversal naming only the payment = %d want 201", code)
	}
	var paid, untouched teamBilling
	f.call("GET", "/api/v1/team/billing", payer.auth, nil, &paid)
	f.call("GET", "/api/v1/team/billing", other.auth, nil, &untouched)
	if paid.BalanceMicro != 600*store.MicroCreditsPerCent || untouched.BalanceMicro != 0 {
		t.Errorf("balances after the reversal: payer %d, other %d", paid.BalanceMicro, untouched.BalanceMicro)
	}
	if code := reverse("pi_unknown"); code != http.StatusBadRequest {
		t.Errorf("a reversal of an unknown payment = %d want 400", code)
	}
}
