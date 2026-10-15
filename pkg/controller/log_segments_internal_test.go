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
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateRun(t.Context(), store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, nil)
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
	now := time.Unix(100, 0)
	for _, outcome := range []store.NodeState{store.NodeFailed, store.NodeSuccess, store.NodeSkipped, store.NodeCancelled} {
		for _, finished := range []*time.Time{nil, &now} {
			view := store.RunView{Snapshot: store.RunSnapshot{Run: store.RunRow{FinishedAt: finished}, Nodes: []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusDone, Outcome: outcome}}}}
			settled, known := logNodeSettled(view, "job")
			if want := finished != nil || outcome != store.NodeFailed; !known || settled != want {
				t.Fatalf("outcome=%s finished=%v: settled=%v known=%v", outcome, finished, settled, known)
			}
			if settled, known := logNodeSettled(view, "missing"); settled || known {
				t.Fatal("unknown job settled")
			}
		}
	}
}
