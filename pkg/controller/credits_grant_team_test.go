package controller_test

import (
	"net/http"
	"testing"
)

type creditBalance struct {
	BalanceMicro int64 `json:"balance_micro"`
}

func TestCreditsGrant_FundsTheTeamTheOperatorNames(t *testing.T) {
	f := newIdentityFixture(t)
	admin := "Bearer " + f.admin
	signed := f.signIn(person("sub-demo", "demo@example.test", "Demo"))
	team := signed.ActiveTeam.Slug
	grant := func(body map[string]any) int {
		t.Helper()
		body["kind"], body["amount_micro"] = "free", int64(5_000_000)
		return f.call("POST", "/api/v1/credits/grants", admin, body, nil)
	}

	if code := grant(map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("an unnamed grant on a multi-team controller = %d want 400", code)
	}
	if code := grant(map[string]any{"team": "no-such-team"}); code != http.StatusNotFound {
		t.Errorf("a grant to an unregistered team = %d want 404", code)
	}
	if code := grant(map[string]any{"team": team, "reference": "demo-1"}); code != http.StatusCreated {
		t.Fatalf("a grant to %s = %d want 201", team, code)
	}

	var named, own, operator creditBalance
	if code := f.call("GET", "/api/v1/credits/teams/"+team, admin, nil, &named); code != http.StatusOK {
		t.Fatalf("operator read of %s = %d", team, code)
	}
	if code := f.call("GET", "/api/v1/credits", sessionAuth(signed.SessionID), nil, &own); code != http.StatusOK {
		t.Fatalf("team read of its own balance = %d", code)
	}
	if code := f.call("GET", "/api/v1/credits", admin, nil, &operator); code != http.StatusOK {
		t.Fatalf("operator read of default = %d", code)
	}
	if named.BalanceMicro != 5_000_000 || own.BalanceMicro != 5_000_000 {
		t.Errorf("%s balance: operator read %d, team read %d; want 5000000 each", team, named.BalanceMicro, own.BalanceMicro)
	}
	if operator.BalanceMicro != 0 {
		t.Errorf("the default team gained %d from a grant to %s", operator.BalanceMicro, team)
	}
	if code := f.call("GET", "/api/v1/credits/teams/"+team, sessionAuth(signed.SessionID), nil, nil); code != http.StatusForbidden {
		t.Errorf("a team owner read the operator's per-team balance route: %d", code)
	}
}

func TestCreditsGrant_ASingleTeamControllerStillFundsDefault(t *testing.T) {
	f := newIdentityFixtureWith(t, fixtureOpts{})
	admin := "Bearer " + f.admin
	if code := f.call("POST", "/api/v1/credits/grants", admin,
		map[string]any{"kind": "free", "amount_micro": int64(1_000_000)}, nil); code != http.StatusCreated {
		t.Fatalf("an unnamed grant on a single-team controller = %d want 201", code)
	}
	var bal creditBalance
	if code := f.call("GET", "/api/v1/credits", admin, nil, &bal); code != http.StatusOK || bal.BalanceMicro != 1_000_000 {
		t.Errorf("default balance = %d (%d), want 1000000", bal.BalanceMicro, code)
	}
}
