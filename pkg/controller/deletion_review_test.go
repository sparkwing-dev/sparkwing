package controller_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A cache grant minted before the deletion keeps writing into the team's
// tree until it expires, so the pass deletes the tree again once every such
// grant has lapsed.
func TestTeamDeletionDeletesTheCacheTreeAgainAfterGrantsLapse(t *testing.T) {
	cache, ts := newStorageFakes(t)
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

// A log upload URL issued before the deletion stays valid after the purge
// and can write the team's logs again, so the recheck deletes them too.
func TestTeamDeletionDeletesLogsAgainAfterUploadURLsLapse(t *testing.T) {
	_, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	f.store.SetOutputDir(t.TempDir())
	objs, err := fs.NewArtifactStore(f.store.OutputDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	ctx := context.Background()
	start := time.Now()
	f.srv.ProcessTeamDeletions(ctx, start.Add(5*time.Minute))
	key := logseg.SegmentKey("logs/"+logseg.ClassStandard+"/teams/acme/runs/run-1/a/a1-g1/", 1)
	if err := objs.Put(ctx, key, strings.NewReader("written through a URL issued before the purge")); err != nil {
		t.Fatal(err)
	}
	f.srv.ProcessTeamDeletions(ctx, start.Add(8*time.Hour))
	if ok, err := objs.Has(ctx, key); err != nil || ok {
		t.Fatalf("a log written after the purge survives the recheck: %v, %v", ok, err)
	}
}

// Another replica may still accept a revoked token from its cache, so the
// purge waits out at least twice that cache's lifetime.
func TestTeamPurgeWaitsOutTheTokenCache(t *testing.T) {
	cache, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
	owner := f.user("o", "olga@example.com")
	f.createTeam(&owner, "acme")
	f.seedRun("acme", "run-acme")
	if code := f.call("DELETE", "/api/v1/team", owner.auth, map[string]string{"confirm_slug": "acme"}, nil); code != http.StatusAccepted {
		t.Fatalf("delete = %d", code)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(90*time.Second))
	if got := cache.seen(); len(got) != 0 {
		t.Fatalf("a pass 90s after the request purged: %v", got)
	}
	f.srv.ProcessTeamDeletions(context.Background(), time.Now().Add(3*time.Minute))
	if got := cache.seen(); len(got) != 1 {
		t.Fatalf("a pass 3 minutes after the request = %v, want the purge", got)
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
	_, ts := newStorageFakes(t)
	f := newDeletionFixture(t, ts)
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
	if _, err := f.store.HoldTeamForDispute(context.Background(), "held", "dp_held", "", "dispute opened", time.Now()); err != nil {
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
