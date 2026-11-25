package controller_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func wantIdleAdvice(t *testing.T, srv *controller.Server, team store.Team) {
	t.Helper()
	if got := controller.ClaimPollAdvice(srv, team); got == 0 {
		t.Fatalf("team %s got no idle advice; its runners would keep polling every 500 ms", team)
	}
}

func wantOwnCadence(t *testing.T, srv *controller.Server, team store.Team) {
	t.Helper()
	if got := controller.ClaimPollAdvice(srv, team); got != 0 {
		t.Fatalf("team %s got idle advice %s right after work was queued for it; its runners must return to their own cadence", team, got)
	}
}

func TestARunSubmittedThroughTheAPIReturnsItsTeamsRunnersToTheirOwnCadence(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	controller.IdleSince(f.srv, time.Now().Add(-5*time.Minute))
	wantIdleAdvice(t, f.srv, store.DefaultTeam)

	floodRun(t, f.submit(f.token, "build", fortyHex(1)), http.StatusCreated)
	wantOwnCadence(t, f.srv, store.DefaultTeam)
	wantIdleAdvice(t, f.srv, "another-team")
}

func TestARetryReturnsItsTeamsRunnersToTheirOwnCadence(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	first := floodRun(t, f.submit(f.token, "build", fortyHex(1)), http.StatusCreated)
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	idle := time.Now().Add(-5 * time.Minute)
	controller.IdleSince(f.srv, idle)
	controller.TeamIdleSince(f.srv, store.DefaultTeam, idle)
	wantIdleAdvice(t, f.srv, store.DefaultTeam)

	body, err := json.Marshal(controllerwire.RetryRequest{Mode: "all", IdempotencyKey: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+first.RunID+"/retry", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	floodRun(t, f.serve(req), http.StatusOK)
	wantOwnCadence(t, f.srv, store.DefaultTeam)
	wantIdleAdvice(t, f.srv, "another-team")
}

func TestAnAppWebhookRunReturnsOnlyItsTeamsRunnersAndTheLauncherToTheirOwnCadence(t *testing.T) {
	f := newAppFixture(t)
	olga, bob := f.ghUser(501, "olga"), f.ghUser(502, "bob")
	f.connect(olga, 501, 7, acmeAdmin)
	f.connect(bob, 502, 8, nil)
	team, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := team.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100 * store.MicroCreditsPerCredit, Reference: "app-team-fixture", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if code := f.subscribe(olga, "acme/widgets", "build", nil); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	controller.IdleSince(f.srv, time.Now().Add(-5*time.Minute))
	for _, team := range []store.Team{store.Team(olga.team), store.Team(bob.team), ""} {
		wantIdleAdvice(t, f.srv, team)
	}

	if code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), ""); code != http.StatusAccepted || out["status"] != "dispatched" {
		t.Fatalf("push = %d %v", code, out)
	}
	wantOwnCadence(t, f.srv, store.Team(olga.team))
	wantOwnCadence(t, f.srv, "")
	wantIdleAdvice(t, f.srv, store.Team(bob.team))
}
