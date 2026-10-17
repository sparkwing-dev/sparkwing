package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestTeamPurgeWaitsForDeletionDelay(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(30*time.Second))
	if _, err := f.store.TeamInfo(t.Context(), "acme"); err != nil {
		t.Fatalf("a pass 30s after the request purged the team: %v", err)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(3*time.Minute))
	if _, err := f.store.TeamInfo(t.Context(), "acme"); err == nil {
		t.Fatal("a pass 3 minutes after the request left the team registered")
	}
}

func TestDeleteAccountRetiredSelfServiceCannotMutate(t *testing.T) {
	f := newIdentityFixture(t)
	u := f.user("u", "uma@example.com")
	for _, age := range []time.Duration{0, time.Hour} {
		if _, err := f.store.DB().Exec(`UPDATE sessions SET created_at = ?`, time.Now().Add(-age).UnixNano()); err != nil {
			t.Fatal(err)
		}
		if code := f.call("DELETE", "/api/v1/me", u.auth, map[string]string{"confirm_email": "uma@example.com"}, nil); code != http.StatusMethodNotAllowed {
			t.Fatalf("retired deletion with sign-in age %s = %d, want 405", age, code)
		}
		if me := f.me(u.auth); me.User.ID != u.id || me.ActiveTeam == nil || me.ActiveTeam.Slug != u.team {
			t.Fatalf("retired deletion changed account: %+v", me)
		}
	}
}

// An account left owning the default team gets a refusal it can act on,
// not a server error.
func TestDeleteAccountOwningTheDefaultTeamIsABadRequest(t *testing.T) {
	f := newIdentityFixture(t)
	u := f.user("u", "uma@example.com")
	if _, err := f.store.DB().Exec(`INSERT INTO memberships (team, account_id, role, created_at) VALUES ('default', ?, 'owner', 1)`, u.id); err != nil {
		t.Fatal(err)
	}
	if code := f.call("DELETE", "/api/v1/operator/accounts/uma@example.com", f.consoleAuth(), nil, nil); code != http.StatusBadRequest {
		t.Fatalf("delete while sole owner of default = %d, want 400", code)
	}
	if me := f.me(u.auth); me.User.ID != u.id {
		t.Fatalf("refused deletion changed account: %+v", me)
	}
}

// Deleting a team with money in flight answers 409 with the remedy, from the
// owner's route and the operator's, and neither closes the team.
func TestDeleteTeamRefusesAnOpenCheckoutOrADisputeHold(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('pending','acme','pending','test','usd',100,?,1,?)`, store.MicroCreditsPerCredit, time.Now().Add(time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	var refused struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Error.Message, "wait for the checkout to expire or complete") {
		t.Fatalf("deleting a team with an open checkout = %d %q, want 409 naming the remedy", code, refused.Error.Message)
	}
	if code := f.call("DELETE", "/api/v1/operator/teams/acme", f.consoleAuth(), nil, nil); code != http.StatusConflict {
		t.Fatalf("the operator deleting it = %d, want 409", code)
	}

	f.createTeam(&owner, "held")
	if _, err := f.store.DB().ExecContext(context.Background(), "INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at) VALUES (?,?,?,?,?)", "dp_held", "held", "", "dispute opened", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "held"}, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Error.Message, "contact support") {
		t.Fatalf("deleting a held team = %d %q, want 409 naming the remedy", code, refused.Error.Message)
	}
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 0 {
		t.Fatalf("a refused deletion was recorded: %+v", mine)
	}
}
