package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A node's attempt history is read through its lineage root, so a root the
// creator could name would open another team's history to it. A node's root
// is its own run unless the server recorded the run as a retry.
func TestTeamBoundary_ANodeCannotAdoptAnotherTeamsAttemptHistory(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := context.Background()
		if _, err := f.st.DB().ExecContext(ctx, `INSERT INTO node_execution_attempts
  (team, lineage_root_run_id, run_id, node_id, attempt_ordinal, claim_generation, coordinator_id, membership_id,
   executor_kind, executor_name, executor_id, executor_location, holder_id, reservation_id, started_at,
   outcome, failure_reason)
VALUES ($1, $2, $2, 'n1', 1, 1, 'coord-a', 'member-a', 'runner', 'team-a-box', 'exec-a', 'self-hosted',
   'holder-a', 'res-a', $3, 'failed', 'team A private failure')`,
			string(f.teamA.Team()), f.runA, time.Now().UnixNano()); err != nil {
			t.Fatalf("seed team A's attempt: %v", err)
		}
		if _, body := f.do(http.MethodGet, "/api/v1/runs/"+f.runA+"/nodes", f.ownerA, nil); !strings.Contains(body, "team A private failure") {
			t.Fatalf("team A cannot read its own attempt, so its absence below would prove nothing: %s", body)
		}

		const runB = "run-b-adopts-root"
		gen := f.claimRun(f.runnerB, runB, "")
		raw, _ := json.Marshal(map[string]any{"id": "n1", "status": "pending", "retry_root_run_id": f.runA})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/api/v1/runs/"+runB+"/nodes", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", f.runnerB)
		req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(gen, 10))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("team B creates its node = %d: %s", resp.StatusCode, out)
		}

		node, err := f.st.GetNode(ctx, runB, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if node.RetryRootRunID != runB {
			t.Errorf("team B's node took lineage root %q from its request; want its own run %q", node.RetryRootRunID, runB)
		}
		for _, path := range []string{"/api/v1/runs/" + runB + "/nodes", "/api/v1/runs/" + runB + "/nodes/n1", "/api/v1/runs/" + runB + "?include=nodes"} {
			code, body := f.do(http.MethodGet, path, f.everyScopeB, nil)
			if code != http.StatusOK || strings.Contains(body, "team A private failure") || strings.Contains(body, "team-a-box") {
				t.Errorf("GET %s as team B = %d, and must not carry team A's attempt: %s", path, code, body)
			}
		}
	})
}
