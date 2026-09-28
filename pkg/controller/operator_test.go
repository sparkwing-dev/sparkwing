package controller_test

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func operatorFixture(t *testing.T) (f *identityFixture, operator, owner signedIn) {
	t.Helper()
	f, _ = billingFixture(t)
	operator = f.user("op", "korey@example.com")
	owner = f.user("o", "olga@example.com")
	f.srv.WithOperatorAccounts([]string{" " + operator.id + " "})
	return f, operator, owner
}

func operatorRoutes(team string) [][2]string {
	base := "/api/v1/operator/teams/" + team
	return [][2]string{
		{"GET", "/api/v1/operator/session"},
		{"GET", "/api/v1/operator/teams?q=olga"},
		{"GET", base},
		{"POST", base + "/trust"},
		{"POST", base + "/grants"},
		{"POST", base + "/freeze"},
		{"POST", base + "/unfreeze"},
	}
}

func TestOperatorConsole_RefusesEveryCallerButTheListedOperatorsSession(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	body := map[string]any{"trust": "granted", "reason": "mine", "amount_cents": 100}
	for _, route := range operatorRoutes(owner.team) {
		for _, tc := range []struct {
			name, auth string
			want       int
		}{
			{"anonymous", "", http.StatusUnauthorized},
			{"admin token", "Bearer " + f.admin, http.StatusForbidden},
			{"the team's own owner", owner.auth, http.StatusForbidden},
		} {
			if code := f.call(route[0], route[1], tc.auth, body, nil); code != tc.want {
				t.Errorf("%s: %s %s = %d want %d", tc.name, route[0], route[1], code, tc.want)
			}
		}
	}
	if code := f.call("GET", "/api/v1/operator/session", operator.auth, nil, nil); code != http.StatusOK {
		t.Errorf("the operator's session = %d want 200", code)
	}

	var trust struct {
		Trust string `json:"trust"`
	}
	if code := f.call("GET", "/api/v1/teams/"+owner.team+"/trust", "Bearer "+f.admin, nil, &trust); code != http.StatusOK || trust.Trust != "automatic" {
		t.Errorf("an owner's refused self-trust changed the team: %d %+v", code, trust)
	}

	f.srv.WithOperatorAccounts(nil)
	if code := f.call("GET", "/api/v1/operator/session", operator.auth, nil, nil); code != http.StatusForbidden {
		t.Errorf("a delisted operator's session = %d want 403", code)
	}
}

type operatorTeam struct {
	Team         string   `json:"team"`
	Owners       []string `json:"owners"`
	BalanceMicro int64    `json:"balance_micro"`
	Frozen       bool     `json:"frozen"`
	Holds        []string `json:"holds"`
	Billing      struct {
		Trust              string `json:"trust"`
		TrustBy            string `json:"trust_by"`
		TrustReason        string `json:"trust_reason"`
		PurchaseLimitCents int64  `json:"purchase_limit_cents"`
	} `json:"billing"`
}

func TestOperatorConsole_FindsATeamAndActsOnItWithAReason(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	base := "/api/v1/operator/teams/" + owner.team

	var found struct {
		Teams []struct {
			Team   string   `json:"team"`
			Owners []string `json:"owners"`
		} `json:"teams"`
	}
	if code := f.call("GET", "/api/v1/operator/teams?q=OLGA%40", operator.auth, nil, &found); code != http.StatusOK ||
		len(found.Teams) != 1 || found.Teams[0].Team != owner.team || found.Teams[0].Owners[0] != "olga@example.com" {
		t.Fatalf("search by owner email = %d %+v", code, found)
	}
	if code := f.call("GET", "/api/v1/operator/teams?q=%25", operator.auth, nil, &found); code != http.StatusOK || len(found.Teams) != 0 {
		t.Errorf("a literal %% matched %+v", found.Teams)
	}
	if code := f.call("GET", "/api/v1/operator/teams/nobody", operator.auth, nil, nil); code != http.StatusNotFound {
		t.Errorf("an unknown team = %d want 404", code)
	}

	for _, path := range []string{"/trust", "/grants", "/freeze", "/unfreeze"} {
		if code := f.call("POST", base+path, operator.auth, map[string]any{"trust": "granted", "amount_cents": 100, "reason": "  "}, nil); code != http.StatusBadRequest {
			t.Errorf("%s with no reason = %d want 400", path, code)
		}
	}

	if code := f.call("POST", base+"/trust", operator.auth, map[string]any{"trust": "granted", "reason": "known customer", "limit_cents": 250_000}, nil); code != http.StatusOK {
		t.Fatalf("trust = %d", code)
	}
	if code := f.call("POST", base+"/grants", operator.auth, map[string]any{"amount_cents": 500_001, "reason": "too much"}, nil); code != http.StatusBadRequest {
		t.Errorf("a grant over $5,000 = %d want 400", code)
	}
	grant := map[string]any{"amount_cents": 2_000, "reason": "launch promo", "key": "k1"}
	for range 2 {
		if code := f.call("POST", base+"/grants", operator.auth, grant, nil); code != http.StatusCreated {
			t.Fatalf("grant = %d", code)
		}
	}
	if code := f.call("POST", base+"/freeze", operator.auth, map[string]any{"reason": "abuse report"}, nil); code != http.StatusOK {
		t.Fatalf("freeze = %d", code)
	}

	var team operatorTeam
	if code := f.call("GET", base, operator.auth, nil, &team); code != http.StatusOK {
		t.Fatalf("read = %d", code)
	}
	if team.Team != owner.team || len(team.Owners) != 1 || team.Owners[0] != "olga@example.com" ||
		team.BalanceMicro != 2_000*store.MicroCreditsPerCent || !team.Frozen || len(team.Holds) != 1 ||
		team.Billing.Trust != "granted" || team.Billing.TrustBy != "korey@example.com" ||
		team.Billing.TrustReason != "known customer" || team.Billing.PurchaseLimitCents != 250_000 {
		t.Errorf("after the actions = %+v", team)
	}

	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"reason": "resolved"}, &team); code != http.StatusOK || team.Frozen || len(team.Holds) != 0 {
		t.Errorf("unfreeze = %d %+v", code, team)
	}
}
