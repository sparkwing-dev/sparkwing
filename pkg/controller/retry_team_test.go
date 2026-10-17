package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
		seedRetry(f.teamB, "b-retries-a", f.runA)
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
