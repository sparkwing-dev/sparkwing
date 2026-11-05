package controller_test

import (
	"net/http"
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
