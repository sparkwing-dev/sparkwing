package controller_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestApprovedAccountWithoutTeamCreatesFirstTeam(t *testing.T) {
	f := newIdentityFixture(t)
	f.setSignUp(map[string]any{"mode": "waitlist"})
	newcomer := f.signIn(person("first-team", "new@example.com", "New"))
	auth := sessionAuth(newcomer.SessionID)
	if code := f.call("POST", "/api/v1/teams", auth, map[string]string{"slug": "first-team"}, nil); code != http.StatusForbidden {
		t.Fatalf("waitlisted create team = %d, want 403", code)
	}
	// safety: approval normally provisions a personal team. Retain an approved account
	// without one to cover accounts admitted without that provisioning.
	if _, err := teststore.Exec(t.Context(), f.store, `UPDATE accounts SET waitlisted_at=0 WHERE id=?`, newcomer.User.ID); err != nil {
		t.Fatal(err)
	}
	if me := f.me(auth); me.Waitlisted || me.ActiveTeam != nil || len(me.Memberships) != 0 {
		t.Fatalf("approved account before creation = %+v", me)
	}
	for _, path := range []string{"/api/v1/runs", "/api/v1/approvals"} {
		if code := f.call("GET", path, auth, nil, nil); code != http.StatusForbidden {
			t.Fatalf("no-team GET %s = %d, want 403", path, code)
		}
	}
	var team struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if code := f.call("POST", "/api/v1/teams", auth, map[string]string{"slug": "first-team", "display_name": "First Team"}, &team); code != http.StatusCreated {
		t.Fatalf("approved create team = %d, want 201", code)
	}
	if team.Slug != "first-team" || team.DisplayName != "First Team" || team.Role != "owner" {
		t.Fatalf("created team = %+v", team)
	}
	me := f.me(auth)
	if me.Waitlisted || me.ActiveTeam == nil || me.ActiveTeam.Slug != team.Slug || me.ActiveTeam.Role != "owner" || len(me.Memberships) != 1 {
		t.Fatalf("account after creation = %+v", me)
	}
	for _, path := range []string{"/api/v1/runs", "/api/v1/approvals"} {
		if code := f.call("GET", path, auth, nil, nil); code != http.StatusOK {
			t.Fatalf("created team GET %s = %d, want 200", path, code)
		}
	}
}

func TestFourthUnpaidTeamRefusalNamesTheLimitAndRemedy(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.signIn(person("unpaid-owner", "unpaid@example.com", "Unpaid"))
	auth := sessionAuth(owner.SessionID)
	for _, slug := range []string{"unpaid-two", "unpaid-three"} {
		if code := f.call("POST", "/api/v1/teams", auth, map[string]string{"slug": slug}, nil); code != http.StatusCreated {
			t.Fatalf("create %s = %d, want 201", slug, code)
		}
	}
	var refusal struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if code := f.call("POST", "/api/v1/teams", auth, map[string]string{"slug": "unpaid-four"}, &refusal); code != http.StatusUnprocessableEntity {
		t.Fatalf("fourth unpaid team = %d, want 422", code)
	}
	if refusal.Error.Code != "limit_exceeded" || refusal.Error.Details["reason"] != "unpaid_team_limit" ||
		!strings.Contains(refusal.Error.Message, "unpaid team limit is 3 per account") ||
		!strings.Contains(refusal.Error.Message, "you own 3 teams that are unpaid in the current payment mode") ||
		!strings.Contains(refusal.Error.Message, "adding funds to one of them or by deleting or leaving one") ||
		strings.Contains(refusal.Error.Message, "raise") {
		t.Fatalf("fourth unpaid team refusal = %+v", refusal.Error)
	}
}
