package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
		{"GET", "/api/v1/operator/teams?q=olga"},
		{"GET", base},
		{"POST", base + "/trust"},
		{"POST", base + "/grants"},
		{"POST", base + "/freeze"},
		{"POST", base + "/unfreeze"},
		{"GET", "/api/v1/operator/waitlist"},
		{"POST", "/api/v1/operator/waitlist/approve"},
		{"GET", "/api/v1/operator/standing-requests"},
		{"POST", base + "/standing-request/decline"},
	}
}

func TestOperatorConsoleKeepsActionsPrivateAndPlacementReadMinimal(t *testing.T) {
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
			if tc.name == "admin token" && route == [2]string{"GET", "/api/v1/operator/teams/" + owner.team} {
				tc.want = http.StatusOK
			}
			var response map[string]any
			if code := f.call(route[0], route[1], tc.auth, body, &response); code != tc.want {
				t.Errorf("%s: %s %s = %d want %d", tc.name, route[0], route[1], code, tc.want)
			}
			if tc.name == "admin token" && tc.want == http.StatusOK && (len(response) != 2 || response["team"] != owner.team || response["display_name"] == nil) {
				t.Errorf("deployment team projection = %+v", response)
			}
		}
	}
	if code := f.call("GET", "/api/v1/operator/teams?q=olga", operator.auth, nil, nil); code != http.StatusOK {
		t.Errorf("the operator's session = %d want 200", code)
	}

	var team operatorTeam
	if code := f.call("GET", "/api/v1/operator/teams/"+owner.team, operator.auth, nil, &team); code != http.StatusOK || team.Billing.Trust != "automatic" {
		t.Errorf("a refused trust change changed the team: %d %+v", code, team)
	}

	f.srv.WithOperatorAccounts(nil)
	if code := f.call("GET", "/api/v1/operator/teams?q=olga", operator.auth, nil, nil); code != http.StatusForbidden {
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
		Trust       string `json:"trust"`
		TrustBy     string `json:"trust_by"`
		TrustReason string `json:"trust_reason"`
		Standing    string `json:"standing"`
	} `json:"billing"`
	Events []struct {
		Kind  string         `json:"kind"`
		Actor string         `json:"actor"`
		Attrs map[string]any `json:"attrs"`
	} `json:"events"`
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

	if code := f.call("POST", base+"/trust", operator.auth, map[string]any{"trust": "granted", "reason": "known customer"}, nil); code != http.StatusOK {
		t.Fatalf("trust = %d", code)
	}
	if code := f.call("POST", base+"/grants", operator.auth, map[string]any{"amount_cents": 500_001, "reason": "too much"}, nil); code != http.StatusBadRequest {
		t.Errorf("a grant over $5,000 = %d want 400", code)
	}
	if code := f.call("POST", base+"/grants", operator.auth, map[string]any{"amount_cents": 100, "reason": "no key"}, nil); code != http.StatusBadRequest {
		t.Errorf("a grant with no key = %d want 400", code)
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
		team.Billing.Trust != "granted" || team.Billing.TrustBy != operator.id ||
		team.Billing.TrustReason != "known customer" || team.Billing.Standing != "Held" {
		t.Errorf("after the actions = %+v", team)
	}

	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"reason": "resolved"}, &team); code != http.StatusOK || team.Frozen || len(team.Holds) != 0 {
		t.Errorf("unfreeze = %d %+v", code, team)
	}

	if code := f.call("GET", base, operator.auth, nil, &team); code != http.StatusOK {
		t.Fatalf("read = %d", code)
	}
	var history []string
	for _, ev := range team.Events {
		history = append(history, ev.Kind+" by "+ev.Actor+": "+fmt.Sprint(ev.Attrs["reason"]))
	}
	want := []string{
		"team.unfrozen by " + operator.id + ": resolved",
		"team.frozen by " + operator.id + ": abuse report",
		"credit.granted by " + operator.id + ": launch promo",
		"billing.trust_changed by " + operator.id + ": known customer",
		"team.created by " + owner.id + ": <nil>",
	}
	if !slices.Equal(history, want) {
		t.Errorf("history = %q\nwant %q", history, want)
	}
}

func TestOperatorConsole_KeepsARevocationAndDisputeHolds(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	base := "/api/v1/operator/teams/" + owner.team
	revoke := map[string]any{"trust": "revoked", "reason": "chargeback"}
	if code := f.call("POST", base+"/trust", operator.auth, revoke, nil); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	var team operatorTeam
	if code := f.call("GET", base, operator.auth, nil, &team); code != http.StatusOK || team.Billing.Trust != "revoked" {
		t.Fatalf("committed revocation = %d %+v", code, team.Billing)
	}

	if _, err := teststore.SQL(t, f.store).ExecContext(context.Background(), "INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at,cause) VALUES (?,?,?,?,?,'dispute')", "dp_1", owner.team, "", "chargeback", time.Now().UnixNano()); err != nil {
		t.Fatalf("dispute hold: %v", err)
	}
	if code := f.call("POST", base+"/freeze", operator.auth, map[string]any{"reason": "abuse"}, nil); code != http.StatusOK {
		t.Fatalf("freeze = %d", code)
	}
	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"reason": "done"}, &team); code != http.StatusOK ||
		!team.Frozen || !slices.Equal(team.Holds, []string{"dp_1"}) {
		t.Errorf("unfreeze = %d %+v, want only the dispute hold left", code, team)
	}
	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"hold": "dp_9", "reason": "won"}, nil); code != http.StatusNotFound {
		t.Errorf("releasing a hold the team does not hold = %d want 404", code)
	}
	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"hold": "dp_1", "reason": "dispute won"}, &team); code != http.StatusOK ||
		team.Frozen || len(team.Holds) != 0 {
		t.Errorf("releasing the dispute hold = %d %+v, want the team released", code, team)
	}
	if f.call("GET", base, operator.auth, nil, &team); team.Events[0].Kind != store.BusinessEventTeamUnfrozen ||
		team.Events[0].Actor != operator.id || team.Events[0].Attrs["reason"] != "dispute won" || team.Billing.Trust != "revoked" {
		t.Errorf("the dispute release's event = %+v", team.Events[0])
	}
}

func TestOperatorConsoleRejectsRetiredDollarOverrides(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	for _, field := range []string{"limit_cents", "daily_cap_cents", "rung_cents", "runner_cap"} {
		body := map[string]any{"trust": "granted", "reason": "review", field: 100}
		if code := f.call("POST", "/api/v1/operator/teams/"+owner.team+"/trust", operator.auth, body, nil); code != http.StatusBadRequest {
			t.Fatalf("retired %s accepted: %d", field, code)
		}
	}
}

func TestAdminTokenReachesNoOperatorPower(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	admin := "Bearer " + f.admin
	cent := int64(store.MicroCreditsPerCent)
	for _, route := range [][2]string{
		{"POST", "/api/v1/credits/reversals"},
		{"POST", "/api/v1/credits/freezes"},
		{"POST", "/api/v1/credits/cards"},
		{"POST", "/api/v1/credits/card-payments"},
		{"POST", "/api/v1/credits/warnings"},
		{"POST", "/api/v1/credits/card-refunds"},
		{"GET", "/api/v1/credits/units"},
		{"GET", "/api/v1/credits/payments/pi_1"},
	} {
		body := map[string]any{"kind": "free", "amount_micro": 500 * cent, "team": owner.team}
		if code := f.call(route[0], route[1], admin, body, nil); code != http.StatusNotFound {
			t.Errorf("admin token: %s %s = %d want 404", route[0], route[1], code)
		}
	}
	for _, route := range [][2]string{
		{"GET", "/api/v1/teams/" + owner.team + "/trust"},
		{"POST", "/api/v1/teams/" + owner.team + "/trust"},
		{"GET", "/api/v1/signups/waitlist"},
		{"POST", "/api/v1/signups/waitlist/approve"},
	} {
		if code := f.call(route[0], route[1], admin, map[string]any{"trust": "granted", "reason": "x", "oldest": 1}, nil); code != http.StatusNotFound {
			t.Errorf("retired route %s %s = %d want 404", route[0], route[1], code)
		}
	}
	var team operatorTeam
	f.call("GET", "/api/v1/operator/teams/"+owner.team, operator.auth, nil, &team)
	if team.BalanceMicro != 0 || team.Frozen || team.Billing.Trust != "automatic" {
		t.Errorf("the admin token's refused calls changed the team: %+v", team)
	}
}

type operatorWaitlist struct {
	Total    int `json:"total"`
	Accounts []struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	} `json:"accounts"`
}

func (w operatorWaitlist) emails() []string {
	var out []string
	for _, a := range w.Accounts {
		out = append(out, a.Email)
	}
	return out
}

func TestOperatorConsole_ListsTheWaitlistNewestFirstAndApprovesByID(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	f.setSignUp(map[string]any{"mode": "waitlist", "reason": "until launch"})
	ids := map[string]string{}
	for i, name := range []string{"ann", "bob", "cat"} {
		ids[name] = f.signIn(person("w-"+name, name+"@example.com", name)).User.ID
		// safety: a host clock sync can step the wall clock back between two
		// sign-ups, so the order this test reads is pinned rather than taken
		// from the stamps the sign-ups wrote.
		res, err := teststore.SQL(t, f.store).Exec(`UPDATE accounts SET waitlisted_at = ? WHERE id = ?`, int64(i+1), ids[name])
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("pin %s's waitlist order: %d rows", name, n)
		}
	}

	var page operatorWaitlist
	if code := f.call("GET", "/api/v1/operator/waitlist?limit=2", operator.auth, nil, &page); code != http.StatusOK ||
		page.Total != 3 || !slices.Equal(page.emails(), []string{"cat@example.com", "bob@example.com"}) ||
		page.Accounts[0].Provider != "google" || page.Accounts[0].Name != "cat Test" {
		t.Fatalf("first page = %d %+v", code, page)
	}
	if code := f.call("GET", "/api/v1/operator/waitlist?limit=2&offset=2", operator.auth, nil, &page); code != http.StatusOK ||
		!slices.Equal(page.emails(), []string{"ann@example.com"}) {
		t.Fatalf("second page = %d %+v", code, page)
	}
	for _, q := range []string{"limit=0", "limit=1001", "offset=-1", "offset=x"} {
		if code := f.call("GET", "/api/v1/operator/waitlist?"+q, operator.auth, nil, nil); code != http.StatusBadRequest {
			t.Errorf("%s = %d want 400", q, code)
		}
	}

	approve := map[string]any{"account_ids": []string{ids["ann"], ids["cat"]}}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", owner.auth, approve, nil); code != http.StatusForbidden {
		t.Fatalf("a team owner's approval = %d want 403", code)
	}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", "Bearer "+f.admin, approve, nil); code != http.StatusForbidden {
		t.Fatalf("an admin token's approval = %d want 403", code)
	}
	if f.call("GET", "/api/v1/operator/waitlist", operator.auth, nil, &page); page.Total != 3 {
		t.Fatalf("a refused approval admitted someone: %+v", page)
	}

	var approved struct {
		Approved []struct {
			Email      string `json:"email"`
			ActiveTeam string `json:"active_team"`
		} `json:"approved"`
	}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", operator.auth, approve, &approved); code != http.StatusOK ||
		len(approved.Approved) != 2 || approved.Approved[0].ActiveTeam == "" {
		t.Fatalf("approve = %d %+v", code, approved)
	}
	if code := f.call("POST", "/api/v1/operator/waitlist/approve", operator.auth, approve, &approved); code != http.StatusOK || len(approved.Approved) != 0 {
		t.Errorf("approving again = %d %+v, want nobody admitted twice", code, approved)
	}
	if f.call("GET", "/api/v1/operator/waitlist", operator.auth, nil, &page); page.Total != 1 ||
		!slices.Equal(page.emails(), []string{"bob@example.com"}) {
		t.Errorf("waitlist after approving = %+v", page)
	}

	admitted := map[string]string{}
	rows, err := teststore.SQL(t, f.store).Query(`SELECT account, actor, attrs FROM audit_events WHERE kind = ?`, store.BusinessEventAccountAdmitted)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var account, actor, raw string
		var attrs map[string]any
		if err := rows.Scan(&account, &actor, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &attrs); err != nil {
			t.Fatal(err)
		}
		admitted[account] = actor + " via " + fmt.Sprint(attrs["via"])
	}
	want := map[string]string{
		ids["ann"]: operator.id + " via waitlist",
		ids["cat"]: operator.id + " via waitlist",
	}
	for name, id := range ids {
		if admitted[id] != want[id] {
			t.Errorf("%s admission = %q want %q", name, admitted[id], want[id])
		}
	}
}

func TestOperatorConsoleReleasesItsHoldAsNoFault(t *testing.T) {
	f, operator, owner := operatorFixture(t)
	base := "/api/v1/operator/teams/" + owner.team
	var held struct {
		Holds      []string `json:"holds"`
		HoldCauses []string `json:"hold_causes"`
	}
	if code := f.call("POST", base+"/freeze", operator.auth, map[string]any{"reason": "abuse report"}, &held); code != http.StatusOK || len(held.Holds) != 1 || held.HoldCauses[0] != store.HoldCauseOperator {
		t.Fatalf("freeze = %d %+v", code, held)
	}
	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"reason": "x", "hold": "dp_missing", "no_fault": true}, nil); code != http.StatusBadRequest {
		t.Errorf("no-fault release of a payment hold = %d want 400", code)
	}
	if code := f.call("POST", base+"/unfreeze", operator.auth, map[string]any{"reason": "report was mistaken", "hold": held.Holds[0], "no_fault": true}, nil); code != http.StatusOK {
		t.Fatalf("no-fault release = %d", code)
	}
	var team operatorTeam
	if code := f.call("GET", base, operator.auth, nil, &team); code != http.StatusOK || team.Frozen {
		t.Fatalf("read = %d %+v", code, team)
	}
	if ev := team.Events[0]; ev.Kind != store.BusinessEventTeamUnfrozen || ev.Attrs["no_fault"] != true || ev.Actor != operator.id {
		t.Errorf("release audit = %+v", ev)
	}
}
