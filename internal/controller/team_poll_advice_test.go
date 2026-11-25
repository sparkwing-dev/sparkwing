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
