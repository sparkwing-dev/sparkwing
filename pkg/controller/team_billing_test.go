package controller_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const checkoutToken = "checkout-secret"

type fakeCheckout struct {
	mu           sync.Mutex
	requests     []checkoutCall
	status       int
	url          string
	internal     []internalCall
	chargeStatus string
	declineCode  string
	// safety: with twoStep a charge naming no payment intent answers
	// created, as the checkout service does, so both worker calls run.
	twoStep     bool
	fingerprint string
}

type internalCall struct {
	Path string
	Body map[string]any
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
		if r.Method == http.MethodPost && r.URL.Path != "/internal/checkout" {
			fc.serveCard(w, r)
			return
		}
		if r.Method != http.MethodPost {
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

func (fc *fakeCheckout) serveCard(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	fc.mu.Lock()
	fc.internal = append(fc.internal, internalCall{Path: r.URL.Path, Body: body})
	status, decline, twoStep, fingerprint := fc.chargeStatus, fc.declineCode, fc.twoStep, fc.fingerprint
	fc.mu.Unlock()
	if status == "" {
		status = "succeeded"
	}
	if twoStep && r.URL.Path == "/internal/charge" && body["payment_intent"] == "" {
		status = "created"
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/internal/refund" {
		_ = json.NewEncoder(w).Encode(map[string]any{"refund": "re_" + fmt.Sprint(body["payment_intent"]), "status": "succeeded"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url": "https://checkout.stripe.test/c/pay/cs_card", "payment_intent": "pi_" + fmt.Sprint(body["attempt_id"]),
		"status": status, "decline_code": decline, "fingerprint": fingerprint,
	})
}

func (fc *fakeCheckout) internalCalls(path string) []internalCall {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var out []internalCall
	for _, c := range fc.internal {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
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
	Frozen             bool  `json:"frozen"`
	Trusted            bool  `json:"trusted"`
	PurchaseLimitCents int64 `json:"purchase_limit_cents"`
	Purchased30dCents  int64 `json:"purchased_30d_cents"`
	CheckoutEnabled    bool  `json:"checkout_enabled"`
	CanPurchase        bool  `json:"can_purchase"`
	Usage              []struct {
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
	OpenMicro    int64  `json:"open_micro"`
	CapMicro     int64  `json:"cap_micro"`
	AmountMicro  int64  `json:"amount_micro"`
}

func (f *identityFixture) grantTo(team, kind, reference string, micro int64) int {
	f.t.Helper()
	return f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": kind, "amount_micro": micro, "reference": reference, "team": team,
	}, nil)
}

func (f *identityFixture) trust(team string, limitCents int64) {
	f.t.Helper()
	if code := f.call("POST", "/api/v1/teams/"+team+"/trust", "Bearer "+f.admin, map[string]any{
		"trust": "granted", "reason": "test", "limit_cents": limitCents,
	}, nil); code != http.StatusOK {
		f.t.Fatalf("trust %s = %d", team, code)
	}
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
		if b.MicroPerCredit != 100_000 || b.CreditsPerDollar != 1_000 || b.MinBillableSeconds != 60 ||
			b.PurchaseMinCents != 1_000 || b.PurchaseMaxCents != 5_000 ||
			b.Trusted || b.PurchaseLimitCents != 5_000 || b.Purchased30dCents != 2_500 ||
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
		{"a purchase under $10", owner, map[string]any{"amount_cents": 999}, http.StatusBadRequest},
		{"a new team's purchase over $50", owner, map[string]any{"amount_cents": 5_001}, http.StatusBadRequest},
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
	f.trust(owner.team, 500_000)
	if code := f.grantTo(owner.team, store.CreditGrantFree, "gift_big", 4_990*100*store.MicroCreditsPerCent); code != http.StatusCreated {
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
	refusal = capRefusal{}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 1_000}, &refusal); code != http.StatusConflict ||
		refusal.OpenMicro != 1_000*store.MicroCreditsPerCent {
		t.Errorf("a checkout past the cap with one open = %d %+v, want 409 naming the open checkout", code, refusal)
	}
	if code := f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": "paid", "amount_micro": 1_000 * store.MicroCreditsPerCent, "reference": "pi_open",
		"team": owner.team, "checkout": "cs_test_1",
	}, nil); code != http.StatusCreated {
		t.Fatalf("the open checkout's paid grant = %d", code)
	}
	var b teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	if b.BalanceMicro != 5_000*100*store.MicroCreditsPerCent {
		t.Errorf("balance after the paid checkout = %d, want the cap", b.BalanceMicro)
	}
}

// Two owners' checkouts racing for the last room below the cap cannot both
// open, because the cap counts the open one.
func TestTeamBillingCheckout_ConcurrentCheckoutsCannotBothPassTheCap(t *testing.T) {
	f, fc := billingFixture(t)
	owner := f.user("o", "olga@example.com")
	f.trust(owner.team, 500_000)
	if code := f.grantTo(owner.team, store.CreditGrantFree, "gift", 4_600*100*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = f.call("POST", "/api/v1/team/billing/checkout", owner.auth, map[string]any{"amount_cents": 30_000}, nil)
		}(i)
	}
	wg.Wait()
	if !(codes[0] == http.StatusOK && codes[1] == http.StatusConflict) && !(codes[0] == http.StatusConflict && codes[1] == http.StatusOK) {
		t.Fatalf("racing checkouts = %v, want one 200 and one 409", codes)
	}
	if n := len(fc.calls()); n != 1 {
		t.Errorf("the checkout service opened %d sessions, want 1", n)
	}
}

func TestTeamBillingCheckout_ReportsAnUnusableCheckoutService(t *testing.T) {
	f, fc := billingFixture(t)
	owner := f.user("o", "olga@example.com")
	f.trust(owner.team, 500_000)
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
			map[string]any{"amount_cents": 50_000}, nil); code != http.StatusBadGateway {
			t.Errorf("%s = %d want 502", tc.name, code)
		}
	}
	fc.mu.Lock()
	fc.status, fc.url = http.StatusOK, "https://checkout.stripe.test/c/pay/cs_test_1"
	fc.mu.Unlock()
	if code := f.grantTo(owner.team, store.CreditGrantFree, "gift", 4_500*100*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 50_000}, nil); code != http.StatusOK {
		t.Errorf("a checkout after failed sessions = %d want 200: a session that never opened still counts", code)
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

// The grant route holds an operator's free grant to the cap and never a paid
// one: a paid grant follows a verified payment, whose checkout already met it.
func TestCreditsGrant_HoldsOnlyAFreeGrantToTheBalanceCap(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	capMicro := int64(5_000 * 100 * store.MicroCreditsPerCent)
	if code := f.grantTo(owner.team, store.CreditGrantFree, "gift_1", capMicro-store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("a grant one cent under the cap = %d", code)
	}
	var refusal capRefusal
	if code := f.call("POST", "/api/v1/credits/grants", "Bearer "+f.admin, map[string]any{
		"kind": "free", "amount_micro": 2 * store.MicroCreditsPerCent, "reference": "gift_2", "team": owner.team,
	}, &refusal); code != http.StatusConflict || refusal.Code != "balance_cap" {
		t.Fatalf("a free grant past the cap = %d %+v want 409 balance_cap", code, refusal)
	}
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", 2*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Errorf("a paid grant past the cap = %d want 201: the payment already went through", code)
	}
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", 2*store.MicroCreditsPerCent); code != http.StatusOK {
		t.Errorf("a replayed payment = %d want 200: a webhook retry must still succeed", code)
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

// A new team buys at most $50 over 30 days: the checkout past it is refused
// with purchase_limit before any session opens, and the page reads what
// remains. A trusted team buys up to $500 at once.
func TestTeamBillingCheckout_RefusesAPurchaseAboveThePurchaseLimit(t *testing.T) {
	f, fc := billingFixture(t)
	owner := f.user("o", "olga@example.com")
	if code := f.grantTo(owner.team, store.CreditGrantPaid, "pi_1", 3_000*store.MicroCreditsPerCent); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	var refusal struct {
		Code           string `json:"code"`
		Trusted        bool   `json:"trusted"`
		LimitCents     int64  `json:"limit_cents"`
		PurchasedCents int64  `json:"purchased_cents"`
		AmountCents    int64  `json:"amount_cents"`
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_001}, &refusal); code != http.StatusConflict {
		t.Fatalf("a checkout past the limit = %d want 409", code)
	}
	if refusal.Code != "purchase_limit" || refusal.Trusted || refusal.LimitCents != 5_000 ||
		refusal.PurchasedCents != 3_000 || refusal.AmountCents != 2_001 {
		t.Errorf("refusal = %+v", refusal)
	}
	if len(fc.calls()) != 0 {
		t.Error("a checkout refused at the purchase limit still opened a session")
	}
	if code := f.call("POST", "/api/v1/team/billing/checkout", owner.auth,
		map[string]any{"amount_cents": 2_000}, nil); code != http.StatusOK {
		t.Fatalf("a checkout filling the limit = %d want 200", code)
	}
	var b teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	if b.Purchased30dCents != 5_000 || b.PurchaseLimitCents != 5_000 {
		t.Errorf("billing after filling the limit = %+v", b)
	}

	trusted := f.user("t", "tess@example.com")
	f.trust(trusted.team, 0)
	if code := f.call("POST", "/api/v1/team/billing/checkout", trusted.auth,
		map[string]any{"amount_cents": 50_000}, nil); code != http.StatusOK {
		t.Errorf("a trusted team's $500 checkout = %d want 200", code)
	}
	f.call("GET", "/api/v1/team/billing", trusted.auth, nil, &b)
	if !b.Trusted || b.PurchaseMaxCents != 50_000 || b.PurchaseLimitCents != 50_000 {
		t.Errorf("trusted billing = %+v", b)
	}
}

// Only the operator reads and sets a team's trust; a revocation holds the
// team to the new-team limits and names who revoked it and why.
func TestBillingTrust_OperatorGrantsAndRevokes(t *testing.T) {
	f, _ := billingFixture(t)
	owner := f.user("o", "olga@example.com")
	path := "/api/v1/teams/" + owner.team + "/trust"
	if code := f.call("GET", path, owner.auth, nil, nil); code != http.StatusForbidden {
		t.Errorf("an owner's read of trust = %d want 403", code)
	}
	if code := f.call("POST", path, owner.auth, map[string]any{"trust": "granted", "reason": "me"}, nil); code != http.StatusForbidden {
		t.Errorf("an owner granting itself trust = %d want 403", code)
	}
	if code := f.call("POST", path, "Bearer "+f.admin, map[string]any{"trust": "granted"}, nil); code != http.StatusBadRequest {
		t.Errorf("a grant with no reason = %d want 400", code)
	}
	if code := f.call("GET", "/api/v1/teams/nobody/trust", "Bearer "+f.admin, nil, nil); code != http.StatusNotFound {
		t.Errorf("an unknown team's trust = %d want 404", code)
	}

	var got struct {
		Trust              string `json:"trust"`
		Trusted            bool   `json:"trusted"`
		TrustReason        string `json:"trust_reason"`
		TrustBy            string `json:"trust_by"`
		PurchaseLimitCents int64  `json:"purchase_limit_cents"`
	}
	f.trust(owner.team, 0)
	if code := f.call("POST", path, "Bearer "+f.admin, map[string]any{"trust": "revoked", "reason": "chargeback"}, &got); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	if got.Trust != "revoked" || got.Trusted || got.TrustReason != "chargeback" || got.TrustBy == "" || got.PurchaseLimitCents != 5_000 {
		t.Errorf("after revoke = %+v", got)
	}
	got.Trust = ""
	if code := f.call("GET", path, "Bearer "+f.admin, nil, &got); code != http.StatusOK || got.Trust != "revoked" {
		t.Errorf("read after revoke = %d %+v", code, got)
	}
	if code := f.call("POST", path, "Bearer "+f.admin, map[string]any{"reason": "no trust named"}, nil); code != http.StatusBadRequest {
		t.Errorf("a change naming no trust = %d want 400", code)
	}
	if code := f.call("POST", path, "Bearer "+f.admin, map[string]any{"trust": "automatic", "reason": "cleared"}, &got); code != http.StatusOK ||
		got.Trust != "automatic" || got.Trusted || got.TrustReason != "cleared" || got.PurchaseLimitCents != 5_000 {
		t.Errorf("reset = %d %+v, want the automatic rule", code, got)
	}
}
