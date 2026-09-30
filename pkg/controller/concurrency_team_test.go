package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A runner resolving a coalesce follower names its leader in the query. A
// leader in another team must read as absent, or the route reports that
// team's node outcome.
func TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		if err := f.st.FinishNode(ctx, f.runA, "n1", "success", "", nil); err != nil {
			t.Fatal(err)
		}
		claimSeq := f.claimRun(ctx, f.runnerB, "run-by-b", "")

		leaderB := "leader-b"
		seedRun(t, f.teamB, leaderB, "build-b")
		if err := f.st.CreateNode(ctx, store.Node{RunID: leaderB, NodeID: "lead", Status: "running"}); err != nil {
			t.Fatal(err)
		}
		if err := f.st.FinishNode(ctx, leaderB, "lead", "success", "", nil); err != nil {
			t.Fatal(err)
		}

		resolve := func(leaderRun, leaderNode string) map[string]any {
			t.Helper()
			q := url.Values{
				"run_id": {"run-by-b"}, "node_id": {"follower"},
				"leader_run_id": {leaderRun}, "leader_node_id": {leaderNode},
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				f.url+"/api/v1/concurrency/deploy/resolve?"+q.Encode(), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", f.runnerB)
			req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimSeq, 10))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("resolve leader %s/%s = %d: %s", leaderRun, leaderNode, resp.StatusCode, raw)
			}
			var out map[string]any
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}

		if got := resolve(leaderB, "lead"); got["status"] != string(store.WaiterLeaderFinished) || got["leader_outcome"] != "success" {
			t.Fatalf("own-team leader resolve = %v, want leader_finished/success", got)
		}

		foreign := resolve(f.runA, "n1")
		if foreign["status"] != string(store.WaiterCancelled) || foreign["leader_outcome"] != nil {
			t.Errorf("foreign leader resolve = %v, want cancelled with no outcome", foreign)
		}
		missing := resolve("no-such-run", "n1")
		if foreign["status"] != missing["status"] {
			t.Errorf("a foreign leader answers %v, a missing one %v", foreign, missing)
		}
	})
}

func (f *tenancyFixture) claimRun(ctx context.Context, auth, runID, extraRunJSON string) int64 {
	t := f.t
	t.Helper()
	if err := f.teamB.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "build-b", TriggerSource: "api", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	code, body := f.do("POST", "/api/v1/triggers/claim", auth, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("claim as team B's runner = %d: %s", code, body)
	}
	var claimed store.Trigger
	if err := json.Unmarshal([]byte(body), &claimed); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/api/v1/runs", strings.NewReader(
		`{"id":"`+runID+`",`+extraRunJSON+`"pipeline":"build-b","status":"running"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimed.ClaimSeq, 10))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /runs under team B's trigger claim = %d: %s", resp.StatusCode, raw)
	}
	return claimed.ClaimSeq
}

// A trigger naming a parent run walks that run's ancestry for cycle
// detection. A foreign ancestor of the caller's own run ends the walk
// instead of naming its pipeline in a cycle error.
func TestTeamBoundary_TriggerAncestryStaysInTheCallersTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		raw, _, err := f.teamB.CreateToken(ctx, "b-spawner", store.TokenKindRunner, []string{
			controller.ScopeRunsWrite, controller.ScopeRunsState, controller.ScopeTriggersClaim,
		}, 0, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		spawner := "Bearer " + raw
		claimSeq := f.claimRun(ctx, spawner, "child-b", `"parent_run_id":"`+f.runA+`",`)

		trigger := func(pipeline string) (int, string) {
			t.Helper()
			b, _ := json.Marshal(map[string]any{
				"pipeline": pipeline, "parent_run_id": "child-b", "trigger": map[string]any{"source": "api"},
			})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/api/v1/triggers", strings.NewReader(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", spawner)
			req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(claimSeq, 10))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			out, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode, string(out)
		}
		if code, body := trigger("build-b"); code != http.StatusConflict {
			t.Errorf("re-entering the parent's own pipeline = %d, want a cycle: %s", code, body)
		}
		if code, body := trigger("build-a"); code != http.StatusAccepted {
			t.Errorf("team B's ancestry walk read team A's run = %d: %s", code, body)
		}
	})
}
