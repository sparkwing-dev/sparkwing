package controller_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A cache grant minted before the deletion keeps writing into the team's
// tree until it expires, so the pass deletes the tree again once every such
// grant has lapsed.
func TestTeamDeletionDeletesTheCacheTreeAgainAfterGrantsLapse(t *testing.T) {
	_, cache, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	start := time.Now()
	f.srv.ProcessTeamDeletions(context.Background(), start.Add(5*time.Minute))
	f.srv.ProcessTeamDeletions(context.Background(), start.Add(time.Hour))
	if got := cache.seen(); len(got) != 1 {
		t.Fatalf("cache deletes before the grants lapse = %v, want one", got)
	}
	f.srv.ProcessTeamDeletions(context.Background(), start.Add(8*time.Hour))
	if got := cache.seen(); len(got) != 2 {
		t.Fatalf("cache deletes after the grants lapse = %v, want a second", got)
	}
	f.srv.ProcessTeamDeletions(context.Background(), start.Add(9*time.Hour))
	if got := cache.seen(); len(got) != 2 {
		t.Fatalf("cache deletes after the second pass = %v, want no third", got)
	}
}

// Another replica may still accept a revoked token from its cache, so the
// purge waits out at least twice that cache's lifetime.
func TestTeamPurgeWaitsOutTheTokenCache(t *testing.T) {
	logs, _, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(90*time.Second))
	if got := logs.seen(); len(got) != 0 {
		t.Fatalf("a pass 90s after the request purged: %v", got)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(3*time.Minute))
	if got := logs.seen(); len(got) != 1 {
		t.Fatalf("a pass 3 minutes after the request = %v, want the purge", got)
	}
}

// The logs service honors an admin bearer for every team's logs, so the
// controller refuses to spend one on a deletion and records why.
func TestTeamDeletionRefusesAnAdminCredentialForLogs(t *testing.T) {
	logs, _, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	f.srv.WithTeamStorage(storageWithLogsToken(ts, f.admin))
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), afterCacheWindow())
	if got := logs.seen(); len(got) != 0 {
		t.Fatalf("the admin token was sent to the logs service: %v", got)
	}
	var mine []teamDeletionBody
	f.call("GET", "/api/v1/me/team-deletions", owner.auth, nil, &mine)
	if len(mine) != 1 || mine[0].State != store.TeamDeletionPending || mine[0].LastError == "" {
		t.Fatalf("deletion = %+v, want pending with the credential problem recorded", mine)
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

func storageWithLogsToken(ts controller.TeamStorage, token string) controller.TeamStorage {
	ts.LogsToken = token
	return ts
}
