package controller

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestAnAcceptedPlanReturnsItsTeamsRunnersToTheirOwnCadence(t *testing.T) {
	e, _, _, raw, body := controllerPlanFixture(t, nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "one", Steps: []nodewire.StepSpec{{ID: "work"}}}, {ID: "two", Steps: []nodewire.StepSpec{{ID: "work"}}}}})
	srv := New(e.Store, nil).WithEngineController(e.EngineController)
	h := srv.Handler()
	srv.recordQueueActivity(time.Now().Add(-testIdleWindow))
	limit := func(nodes string) {
		t.Helper()
		if _, err := teststore.Exec(t.Context(), e.Store, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "compute_limit_"+store.ComputeLimitNodesPerRun, []byte(nodes)); err != nil {
			t.Fatal(err)
		}
	}

	limit("1")
	engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", string(body), http.StatusUnprocessableEntity)
	if got := srv.claimPollAdvice("a", time.Now()); got == 0 {
		t.Fatal("a refused plan, which releases no work, returned the team's runners to their own cadence")
	}

	limit("100")
	engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", string(body), http.StatusOK)
	if got := srv.claimPollAdvice("a", time.Now()); got != 0 {
		t.Fatalf("advice=%s right after an accepted plan released its jobs; the team's runners must return to their own cadence", got)
	}
	if got := srv.claimPollAdvice("another-team", time.Now()); got == 0 {
		t.Fatal("another team's runners lost their idle advice to this team's plan")
	}

	idle := time.Now().Add(-testIdleWindow).UnixNano()
	srv.queueActivity.mu.Lock()
	srv.queueActivity.all, srv.queueActivity.any, srv.queueActivity.teams = idle, idle, nil
	srv.queueActivity.mu.Unlock()
	w := engineClaimHTTP(t, h, raw, http.MethodPost, "/api/v1/runs/run/plan", string(body), http.StatusOK)
	var replay controllerwire.PlanAccepted
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil || !replay.Replayed {
		t.Fatalf("the identical plan was not answered as a replay: %s (%v)", w.Body, err)
	}
	for _, team := range []store.Team{"a", ""} {
		if got := srv.claimPollAdvice(team, time.Now()); got == 0 {
			t.Fatalf("a replayed plan, which releases no jobs, returned claimants of team %q to their own cadence", team)
		}
	}
}
