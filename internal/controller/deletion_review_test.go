package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTeamPurgeWaitsForDeletionDelay(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), teststore.Now(f.store).Add(30*time.Second))
	var runs int
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team='acme'`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("team runs before purge delay = %d: %v", runs, err)
	}
	f.srv.ProcessTeamDeletions(context.Background(), teststore.Now(f.store).Add(3*time.Minute))
	if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team='acme'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("team runs after purge delay = %d: %v", runs, err)
	}
}

func TestDeleteAccountRetiredSelfServiceCannotMutate(t *testing.T) {
	f := newIdentityFixture(t)
	u := f.user("u", "uma@example.com")
	for _, age := range []time.Duration{0, time.Hour} {
		if _, err := teststore.SQL(t, f.store).Exec(`UPDATE sessions SET created_at = ?`, teststore.Now(f.store).Add(-age).UnixNano()); err != nil {
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
	if _, err := teststore.SQL(t, f.store).Exec(`INSERT INTO memberships (team, account_id, role, created_at) VALUES ('default', ?, 'owner', 1)`, u.id); err != nil {
		t.Fatal(err)
	}
	if code := f.call("DELETE", "/api/v1/operator/accounts/uma@example.com", f.consoleAuth(), nil, nil); code != http.StatusBadRequest {
		t.Fatalf("delete while sole owner of default = %d, want 400", code)
	}
	if me := f.me(u.auth); me.User.ID != u.id {
		t.Fatalf("refused deletion changed account: %+v", me)
	}
}

func TestDeleteTeamKeepsAnOpenCheckoutAndADisputeHold(t *testing.T) {
	for _, operator := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "operator"}[operator], func(t *testing.T) {
			f := newDeletionFixture(t)
			owner := f.user("o", "olga@example.com")
			f.createTeam(&owner, "acme")
			if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `INSERT INTO credit_checkouts(id,team,session_id,payment_mode,currency,amount_minor,amount_micro,opened_at,expires_at) VALUES ('pending','acme','pending','test','usd',100,?,1,?)`, store.MicroCreditsPerCredit, teststore.Now(f.store).Add(time.Hour).UnixNano()); err != nil {
				t.Fatal(err)
			}
			if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `INSERT INTO credit_freezes(dispute_id,team,payment_id,reason,created_at,cause) VALUES ('dispute','acme','','dispute opened',1,'dispute')`); err != nil {
				t.Fatal(err)
			}
			path, auth := "/api/v1/team", owner.auth
			if operator {
				path, auth = "/api/v1/operator/teams/acme", f.consoleAuth()
			}
			var del teamDeletionBody
			if code := f.call("DELETE", path, auth, map[string]string{"confirm_slug": "acme"}, &del); code != http.StatusAccepted || del.State != store.TeamDeletionPending {
				t.Fatalf("deletion with money in flight = %d %+v", code, del)
			}
			f.srv.ProcessTeamDeletions(t.Context(), teststore.Now(f.store).Add(3*time.Minute))
			var kept int
			if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM team_deletions WHERE slug='acme' AND state='done'`).Scan(&kept); err != nil || kept != 1 {
				t.Fatalf("finished deletion = %d: %v", kept, err)
			}
			for _, table := range []string{"credit_checkouts", "credit_freezes"} {
				if err := teststore.SQL(t, f.store).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table+` WHERE team='acme'`).Scan(&kept); err != nil || kept != 1 {
					t.Fatalf("retained %s = %d: %v", table, kept, err)
				}
			}
		})
	}
}
