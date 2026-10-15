package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type logSegmentFixture struct {
	dispatchRouteFixture
	srv *Server
	url string
}

func newLogSegmentFixture(t *testing.T) logSegmentFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	if err := st.CreateRun(t.Context(), store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(t.Context(), store.DefaultTeam, "run-d", now); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil).EnableAuthFromStore()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	f := logSegmentFixture{dispatchRouteFixture: dispatchRouteFixture{st: st, handler: srv.Handler()}, srv: srv, url: hs.URL}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f.dispatchRouteFixture, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`},{"id":"b","deps":[],`+dispatchHash+`}]}`,
		http.StatusOK, "accepted")
	return f
}

func TestHeartbeatsReportWhatAViewerWatches(t *testing.T) {
	f := newLogSegmentFixture(t)
	tok := f.claim(t, "a", store.ClaimTokenWork)
	beat := func() bool {
		code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/nodes/a/heartbeat", tok, []byte(`{"lease_secs":60}`))
		var out struct {
			Watched bool `json:"watched"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("claim heartbeat: %d %s", code, raw)
		}
		return out.Watched
	}
	if beat() {
		t.Fatal("an unviewed node reads watched")
	}
	f.srv.liveLogs.Watch("run-d", "a")
	if !beat() {
		t.Fatal("a viewed node reads unwatched")
	}
}

func TestTheLiveRingStartsOverForANewAttempt(t *testing.T) {
	f := newLogSegmentFixture(t)
	admin := f.admin(t)
	push := func(attempt, line string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-d/nodes/a/logs", strings.NewReader(line))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Sparkwing-Log-Offset", "0")
		req.Header.Set("X-Sparkwing-Log-Attempt", attempt)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("live push = %d %s", rec.Code, rec.Body.String())
		}
	}
	push("a1-g1", "first attempt\n")
	push("a2-g2", "second attempt\n")
	chunk, ok := f.srv.liveLogs.Read("run-d", "a", 0)
	if !ok || string(chunk.Data) != "second attempt\n" {
		t.Fatalf("live ring after a second attempt = %q, %v; want only the second attempt's line", chunk.Data, ok)
	}
}

func TestNodeLogSettlesOnlyWithNoRetryToCome(t *testing.T) {
	f := newLogSegmentFixture(t)
	ctx := t.Context()
	set := func(q string) {
		t.Helper()
		if _, err := f.st.DB().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	settled := func() bool {
		t.Helper()
		got, found, err := f.st.NodeLogSettled(ctx, store.DefaultTeam, "run-d", "a")
		if err != nil || !found {
			t.Fatalf("settled = %v, %v, %v", got, found, err)
		}
		return got
	}
	for name, q := range map[string]string{
		"retry backoff with no budget recorded": `UPDATE nodes SET status = 'done', outcome = 'failed', attempts_consumed = 1, retry_budget = 0 WHERE run_id = 'run-d' AND node_id = 'a'`,
		"inline failure":                        `UPDATE nodes SET status = 'done', outcome = 'failed', attempts_consumed = 0, retry_budget = 0 WHERE run_id = 'run-d' AND node_id = 'a'`,
	} {
		set(q)
		if settled() {
			t.Fatalf("%s: a failed node of a running run reads as settled", name)
		}
	}
	set(`UPDATE runs SET status = 'failed' WHERE id = 'run-d'`)
	if !settled() {
		t.Fatal("a failed node of a finished run reads as unsettled")
	}
	set(`UPDATE runs SET status = 'running' WHERE id = 'run-d'`)
	for _, outcome := range []string{"success", "skipped", "cancelled"} {
		set(`UPDATE nodes SET outcome = '` + outcome + `' WHERE run_id = 'run-d' AND node_id = 'a'`)
		if !settled() {
			t.Fatalf("a node done with outcome %s reads as unsettled", outcome)
		}
	}
}
