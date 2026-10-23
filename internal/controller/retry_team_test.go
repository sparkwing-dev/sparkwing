package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// A trigger's retry_of links the new run into another run's attempt tree, and
// the attempts route answers with every run in that tree, so a retry_of naming
// another team's run would hand that run's record to the caller.
func TestTeamBoundary_ATriggerCannotRetryAnotherTeamsRun(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		for _, victim := range []string{f.runA, f.victim} {
			code, body := f.do("POST", "/api/v1/runs/"+victim+"/retry", f.ownerB, controllerwire.RetryRequest{Mode: "all", IdempotencyKey: "foreign-retry"})
			if code != http.StatusNotFound {
				t.Fatalf("retry foreign run %s = %d: %s", victim, code, body)
			}
		}
		code, body := f.do("POST", "/api/v1/runs", f.ownerB, computeIntake("build-b"))
		var own controllerwire.CreateRunResponse
		if code != http.StatusCreated || json.Unmarshal([]byte(body), &own) != nil || own.RunID == "" {
			t.Fatalf("own intake = %d: %s", code, body)
		}
		if err := teststore.SeedRunFinish(f.st, t.Context(), f.teamB.Team(), own.RunID, "failed", "boom"); err != nil {
			t.Fatal(err)
		}
		code, body = f.do("POST", "/api/v1/runs/"+own.RunID+"/retry", f.ownerB, controllerwire.RetryRequest{Mode: "all", IdempotencyKey: "own-retry"})
		if code != http.StatusOK {
			t.Fatalf("own retry = %d: %s", code, body)
		}
		var retried controllerwire.CreateRunResponse
		if err := json.Unmarshal([]byte(body), &retried); err != nil {
			t.Fatal(err)
		}
		code, body = f.do("GET", "/api/v1/runs/"+retried.RunID+"/attempts", f.ownerB, nil)
		ids := attemptIDs(t, body)
		if code != 200 || !ids[own.RunID] || !ids[retried.RunID] || ids[f.runA] || ids[f.victim] {
			t.Fatalf("own attempts = %d: %s", code, body)
		}
	})
}

// A retry_of pointer that already crosses teams, whichever way it points, is
// not followed: the attempt tree stays inside the team that asks for it.
func TestTeamBoundary_AttemptsNeverFollowAPointerIntoAnotherTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		now := time.Now()
		seedRetry := func(tn *store.Tenant, id, retryOf string) {
			t.Helper()
			if err := teststore.SeedRun(f.st, ctx, tn.Team(), store.Run{RunSummary: runstate.RunSummary{ID: id, Pipeline: "p", Status: "running", RetryOf: retryOf, CreatedAt: now, StartedAt: now}}); err != nil {
				t.Fatal(err)
			}
		}
		seedRetry(f.teamB, "b-retries-a", f.runA)
		seedRun(t, f.st, f.teamB, "b-root", "p")
		seedRetry(f.teamA, "a-retries-b", "b-root")

		for _, tc := range []struct{ asked, leaked string }{
			{"b-retries-a", f.runA},
			{"b-root", "a-retries-b"},
		} {
			code, body := f.do("GET", "/api/v1/runs/"+tc.asked+"/attempts", f.ownerB, nil)
			if code != http.StatusOK {
				t.Fatalf("GET /runs/%s/attempts as team B = %d: %s", tc.asked, code, body)
			}
			ids := attemptIDs(t, body)
			if ids[tc.leaked] {
				t.Errorf("GET /runs/%s/attempts as team B lists team A's %s: %s", tc.asked, tc.leaked, body)
			}
			if !ids[tc.asked] {
				t.Errorf("GET /runs/%s/attempts as team B omits the run asked about: %s", tc.asked, body)
			}
		}
	})
}

func attemptIDs(t *testing.T, body string) map[string]bool {
	t.Helper()
	var resp struct {
		Runs []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("attempts body %s: %v", body, err)
	}
	ids := map[string]bool{}
	for _, r := range resp.Runs {
		ids[r.ID] = true
	}
	return ids
}
