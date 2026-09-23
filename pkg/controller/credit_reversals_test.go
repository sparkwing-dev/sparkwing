package controller_test

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (f *identityFixture) creditsGrantToken() string {
	f.t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if code := f.call("POST", "/api/v1/tokens", "Bearer "+f.admin, map[string]any{
		"principal": "checkout-service", "kind": "service", "scopes": []string{controller.ScopeCreditsGrant},
	}, &minted); code != http.StatusCreated {
		f.t.Fatalf("mint a credits.grant token = %d", code)
	}
	return "Bearer " + minted.Token
}

var muxRoute = regexp.MustCompile(`mux\.Handle\("([A-Z]+) ([^"]+)"`)

// The checkout service's credential records a paid grant, reverses a payment,
// holds the team a payment funded and reads the ledger's units, and every
// other route refuses it.
func TestCreditsGrantScopeReachesOnlyTheGrantRoutes(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	grant := f.creditsGrantToken()
	cent := int64(store.MicroCreditsPerCent)

	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 500 * cent, "reference": "pi_1", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("a paid grant = %d want 201", code)
	}
	for _, body := range []map[string]any{
		{"kind": "free", "amount_micro": 500 * cent, "team": owner.team},
		{"kind": "reversal", "amount_micro": -100 * cent, "reference": "re_1", "reverses": "pi_1"},
	} {
		if code := f.call("POST", "/api/v1/credits/grants", grant, body, nil); code != http.StatusForbidden {
			t.Errorf("a %s grant with credits.grant = %d want 403", body["kind"], code)
		}
	}
	if code := f.call("POST", "/api/v1/credits/freezes", grant,
		map[string]any{"team": owner.team, "frozen": true}, nil); code != http.StatusForbidden {
		t.Errorf("freezing a named team with credits.grant = %d want 403", code)
	}
	if code := f.call("GET", "/api/v1/credits/units", grant, nil, nil); code != http.StatusOK {
		t.Errorf("units = %d want 200", code)
	}

	allowed := map[string]bool{
		"POST /api/v1/credits/grants":    true,
		"POST /api/v1/credits/reversals": true,
		"POST /api/v1/credits/freezes":   true,
		"GET /api/v1/credits/units":      true,
		// safety: both answer any authenticated caller, the first with its own
		// identity and the second with the service URLs.
		"GET /api/v1/auth/whoami": true,
		"GET /api/v1/services":    true,
	}
	now := time.Now()
	if err := f.store.CreateTriggerWithRun(context.Background(),
		store.Trigger{ID: "rx", Pipeline: "build", CreatedAt: now},
		store.Run{ID: "rx", Pipeline: "build", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, m := range muxRoute.FindAllStringSubmatch(string(src), -1) {
		route := m[1] + " " + m[2]
		if allowed[route] {
			continue
		}
		path := strings.ReplaceAll(m[2], "{id}", "rx")
		path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x")
		path = strings.TrimSuffix(path, "...")
		// safety: the pool and artifact routes are registered only on a server
		// given a pool or an artifact store, which this fixture is not.
		if strings.HasPrefix(m[2], "/api/v1/pool") || strings.HasPrefix(m[2], "/api/v1/artifacts/") {
			continue
		}
		code := f.call(m[1], path, grant, map[string]any{}, nil)
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("%s with a credits.grant token = %d, want 401 or 403", route, code)
		}
		checked++
	}
	if checked < 50 {
		t.Fatalf("checked %d routes; the route pattern no longer matches server.go", checked)
	}
}

// A team's owner cannot mint the checkout service's scope into a team token.
func TestCreditsGrantScopeIsTheOperatorsToMint(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	if code := f.call("POST", "/api/v1/team/runner-tokens", owner.auth, map[string]any{
		"name": "sneaky", "scopes": []string{controller.ScopeCreditsGrant},
	}, nil); code < 400 {
		t.Errorf("a team runner token carrying credits.grant = %d, want a refusal", code)
	}
	if code := f.call("POST", "/api/v1/tokens", owner.auth, map[string]any{
		"principal": "x", "kind": "service", "scopes": []string{controller.ScopeCreditsGrant},
	}, nil); code != http.StatusForbidden {
		t.Errorf("an owner minting credits.grant = %d, want 403", code)
	}
}

// An operator's refund and a lost chargeback reverse what a payment still
// has, in the team it funded; a freeze named by payment holds that team's
// metered work until it is released.
func TestReversalsAndFreezesFollowThePayment(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	other := f.user("x", "xena@example.com")
	grant := f.creditsGrantToken()
	cent := int64(store.MicroCreditsPerCent)
	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 400 * cent, "reference": "pi_1", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}

	var frozen struct {
		Team   string `json:"team"`
		Frozen bool   `json:"frozen"`
	}
	if code := f.call("POST", "/api/v1/credits/freezes", grant, map[string]any{
		"payment_id": "pi_1", "frozen": true, "reason": "dispute dp_1 opened",
	}, &frozen); code != http.StatusOK || frozen.Team != owner.team || !frozen.Frozen {
		t.Fatalf("freeze by payment = %d %+v", code, frozen)
	}
	var b teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	var ob teamBilling
	f.call("GET", "/api/v1/team/billing", other.auth, nil, &ob)
	if !b.Frozen || ob.Frozen {
		t.Errorf("frozen: payer %v, other %v; want only the payer held", b.Frozen, ob.Frozen)
	}

	var rev struct {
		Team          string `json:"team"`
		ReversedMicro int64  `json:"reversed_micro"`
		BalanceMicro  int64  `json:"balance_micro"`
		Created       bool   `json:"created"`
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_1", "reference": "dp_1",
	}, &rev); code != http.StatusCreated || rev.Team != owner.team || rev.ReversedMicro != 400*cent || rev.BalanceMicro != 0 {
		t.Fatalf("reversal = %d %+v", code, rev)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", "Bearer "+f.admin, map[string]any{
		"payment_id": "pi_1", "reference": "refund:pi_1",
	}, &rev); code != http.StatusOK || rev.Created {
		t.Errorf("a refund of a payment already reversed = %d %+v, want 200 with nothing written", code, rev)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", "Bearer "+f.admin, map[string]any{
		"payment_id": "pi_unknown", "reference": "refund:pi_unknown",
	}, nil); code != http.StatusNotFound {
		t.Errorf("an unknown payment = %d want 404", code)
	}

	if code := f.call("POST", "/api/v1/credits/freezes", "Bearer "+f.admin, map[string]any{
		"team": owner.team, "frozen": false,
	}, &frozen); code != http.StatusOK || frozen.Frozen {
		t.Fatalf("release by team = %d %+v", code, frozen)
	}
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	if b.Frozen {
		t.Error("the team is still frozen after the release")
	}
}
