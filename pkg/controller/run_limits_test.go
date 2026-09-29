package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAnnotationAndMetricBoundsAnswerWithTheirStatus(t *testing.T) {
	f := newOwnershipFixtureWithScopes(t, []string{controller.ScopeNodesClaim, controller.ScopeRunsState})
	big, err := json.Marshal(map[string]string{"message": strings.Repeat("x", store.MaxAnnotationBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/annotations", string(big)); got != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized annotation = %d, want 413", got)
	}
	full, err := json.Marshal(make([]string, store.MaxRunAnnotations))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().Exec(`UPDATE runs SET annotations_json = ? WHERE id = 'run-1'`, full); err != nil {
		t.Fatal(err)
	}
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/annotations", `{"message":"hi"}`); got != http.StatusTooManyRequests {
		t.Errorf("a node annotation past the run's count = %d, want 429", got)
	}
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/steps/annotations", `{"step_id":"s1","message":"hi"}`); got != http.StatusTooManyRequests {
		t.Errorf("a step annotation past the run's count = %d, want 429", got)
	}

	seedNodeMetrics(t, f.store, store.MaxNodeMetricSamples)
	if got := f.post(t, f.owner, "/api/v1/runs/run-1/nodes/only/metrics", `{"cpu_millicores":1,"memory_bytes":1}`); got != http.StatusTooManyRequests {
		t.Errorf("a metric sample past the node's cap = %d, want 429", got)
	}
}

func TestNodeMetricReadsArePaged(t *testing.T) {
	f := newOwnershipFixtureWithScopes(t, []string{controller.ScopeNodesClaim, controller.ScopeRunsState})
	seedNodeMetrics(t, f.store, 2500)
	get := func(query string) (int, struct {
		Points     []json.RawMessage `json:"points"`
		NextCursor string            `json:"next_cursor"`
	},
	) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, f.url+"/api/v1/runs/run-1/nodes/only/metrics"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.owner)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var page struct {
			Points     []json.RawMessage `json:"points"`
			NextCursor string            `json:"next_cursor"`
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, page
	}
	code, first := get("")
	if code != http.StatusOK || len(first.Points) != 1000 || first.NextCursor == "" {
		t.Fatalf("default page = %d, %d points, cursor %q", code, len(first.Points), first.NextCursor)
	}
	code, last := get("?limit=2000&cursor=" + first.NextCursor)
	if code != http.StatusOK || len(last.Points) != 1500 || last.NextCursor != "" {
		t.Fatalf("last page = %d, %d points, cursor %q", code, len(last.Points), last.NextCursor)
	}
	if code, _ := get("?limit=10001"); code != http.StatusBadRequest {
		t.Errorf("a limit past the cap = %d, want 400", code)
	}
	all, err := client.NewWithToken(f.url, nil, f.owner).ListNodeMetrics(context.Background(), "run-1", "only")
	if err != nil || len(all) != 2500 {
		t.Fatalf("client read = %d samples, %v; want every page", len(all), err)
	}
}

func seedNodeMetrics(t *testing.T, st *store.Store, n int) {
	t.Helper()
	tx, err := st.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0)
	for i := range n {
		if _, err := tx.Exec(`INSERT INTO node_metrics (team, run_id, node_id, ts, cpu_millicores, memory_bytes, cpu_time_nanos)
SELECT team, id, 'only', ?, 1, 1, 0 FROM runs WHERE id = 'run-1'`, base.Add(time.Duration(i)*time.Second).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
