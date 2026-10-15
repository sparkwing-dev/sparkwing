package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Another replica may still accept a revoked token from its cache, so the
// purge waits out at least twice that cache's lifetime.
func TestTeamPurgeWaitsOutTheTokenCache(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(90*time.Second))
	if _, err := f.store.TeamInfo(t.Context(), "acme"); err != nil {
		t.Fatalf("a pass 90s after the request purged the team: %v", err)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(3*time.Minute))
	if _, err := f.store.TeamInfo(t.Context(), "acme"); err == nil {
		t.Fatal("a pass 3 minutes after the request left the team registered")
	}
}

// Deleting an account is refused unless the session signed in recently.
func TestDeleteAccountNeedsARecentSignIn(t *testing.T) {
	f := newIdentityFixture(t)
	u := f.user("u", "uma@example.com")
	if _, err := f.store.DB().Exec(`UPDATE sessions SET created_at = created_at - 3600`); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Code string `json:"error"`
	}
	if code := f.call("DELETE", "/api/v1/me", u.auth, map[string]string{"confirm_email": "uma@example.com"}, &body); code != http.StatusForbidden || body.Code != "reauth_required" {
		t.Fatalf("delete with an hour-old sign-in = %d %+v, want 403 reauth_required", code, body)
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
	if code := f.call("DELETE", "/api/v1/me", u.auth, map[string]string{"confirm_email": "uma@example.com"}, nil); code != http.StatusBadRequest {
		t.Fatalf("delete while sole owner of default = %d, want 400", code)
	}
}

// Deleting a team with money in flight answers 409 with the remedy, from the
// owner's route and the operator's, and neither closes the team.
func TestDeleteTeamRefusesAnOpenCheckoutOrADisputeHold(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	acme, err := f.store.ForTeam(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acme.OpenCreditCheckout(context.Background(), store.MicroCreditsPerCredit, time.Now(), time.Hour); err != nil {
		t.Fatal(err)
	}
	var refused struct {
		Error string `json:"error"`
	}
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Error, "wait for the checkout to expire or complete") {
		t.Fatalf("deleting a team with an open checkout = %d %q, want 409 naming the remedy", code, refused.Error)
	}
	if code := f.call("DELETE", "/api/v1/operator/teams/acme", f.consoleAuth(), nil, nil); code != http.StatusConflict {
		t.Fatalf("the operator deleting it = %d, want 409", code)
	}

	f.createTeam(&owner, "held")
	if _, err := f.store.DB().ExecContext(context.Background(), "INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at) VALUES (?,?,?,?,?)", "dp_held", "held", "", "dispute opened", time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "held"}, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Error, "contact support") {
		t.Fatalf("deleting a held team = %d %q, want 409 naming the remedy", code, refused.Error)
	}
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 0 {
		t.Fatalf("a refused deletion was recorded: %+v", mine)
	}
}
