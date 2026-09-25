package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A trigger's retry_of links the new run into another run's attempt tree, and
// the attempts route answers with every run in that tree, so a retry_of naming
// another team's run would hand that run's record to the caller.
func TestTeamBoundary_ATriggerCannotRetryAnotherTeamsRun(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		for _, victim := range []string{f.runA, f.victim} {
			code, body := f.do("POST", "/api/v1/triggers", f.ownerB, map[string]any{
				"pipeline": "build-b", "trigger": map[string]any{"source": "api"},
				"retry_of": victim,
			})
			if code != http.StatusNotFound {
				t.Errorf("POST /triggers as team B with retry_of %s = %d want 404: %s", victim, code, body)
			}
			var resp struct {
				RunID string `json:"run_id"`
			}
			if json.Unmarshal([]byte(body), &resp) == nil && resp.RunID != "" {
				_, attempts := f.do("GET", "/api/v1/runs/"+resp.RunID+"/attempts", f.ownerB, nil)
				if attemptIDs(t, attempts)[victim] {
					t.Errorf("team B's attempts of %s list %s: %s", resp.RunID, victim, attempts)
				}
			}
		}

		code, body := f.do("POST", "/api/v1/triggers", f.ownerB, map[string]any{
			"pipeline": "build-b", "trigger": map[string]any{"source": "api"},
		})
		if code != http.StatusAccepted {
			t.Fatalf("POST /triggers as team B = %d: %s", code, body)
		}
		var own struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(body), &own); err != nil {
			t.Fatal(err)
		}
		code, body = f.do("POST", "/api/v1/triggers", f.ownerB, map[string]any{
			"pipeline": "build-b", "trigger": map[string]any{"source": "manual"},
			"retry_of": own.RunID,
		})
		if code != http.StatusAccepted {
			t.Fatalf("POST /triggers as team B retrying its own run = %d: %s", code, body)
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
			if err := tn.CreateTriggerWithRun(ctx,
				store.Trigger{ID: id, Pipeline: "p", TriggerSource: "api", RetryOf: retryOf, CreatedAt: now},
				store.Run{ID: id, Pipeline: "p", Status: "running", RetryOf: retryOf, CreatedAt: now, StartedAt: now},
			); err != nil {
				t.Fatal(err)
			}
		}
		// up: team B's run names team A's run as its source.
		seedRetry(f.teamB, "b-retries-a", f.runA)
		// down: team A's run names team B's run as its source.
		seedRun(t, f.teamB, "b-root", "p")
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
