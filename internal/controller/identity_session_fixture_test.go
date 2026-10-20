package controller_test

import (
	"net/http"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestIdentityFixtureUsesPersistedSessionAuthority(t *testing.T) {
	b := newBrowserFixture(t, nil)
	f := &identityFixture{t: t, url: dashOrigin, store: b.st, srv: b.srv}
	var me meBody
	if code := f.call("GET", "/api/v1/me", sessionAuth(b.sessID), nil, &me); code != http.StatusOK || me.User.ID != "alice" {
		t.Fatalf("session identity = %d %+v", code, me)
	}
	property := func(owner bool) bool {
		role := "reader"
		if owner {
			role = "owner"
		}
		if _, err := teststore.SQL(t, b.st).Exec(`DELETE FROM memberships WHERE team='default' AND account_id='alice'`); err != nil {
			t.Fatal(err)
		}
		if _, err := teststore.SQL(t, b.st).Exec(`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','alice',?,1)`, role); err != nil {
			t.Fatal(err)
		}
		code := f.call("POST", "/api/v1/team/invitations", sessionAuth(b.sessID), map[string]string{"email": "invalid", "role": "reader"}, nil)
		if owner {
			return code == http.StatusBadRequest
		}
		return code == http.StatusForbidden
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, b.st).Exec(`DELETE FROM memberships WHERE team='default' AND account_id='alice'`); err != nil {
		t.Fatal(err)
	}
	if w := b.serve(b.cookieRequest("GET", "/api/v1/team/members", "")); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("removed membership read = %d %s", w.Code, w.Body)
	}
}
