package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type segmentController struct {
	st  *store.Store
	url string
}

func newSegmentController(t *testing.T) (segmentController, string) {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, now := t.Context(), time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "run-s", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(ctx, store.DefaultTeam, "run-s", now); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(hs.Close)
	claim := func(nodeID string) string {
		c, err := st.ClaimLaunch(ctx, store.ClaimIdentity{Principal: "launcher", TokenPrefix: "swr_launch"},
			store.LaunchClaimRequest{HolderID: "holder-" + nodeID, Lease: time.Minute, Deadline: time.Hour, RunID: "run-s", NodeID: nodeID}, time.Now())
		if err != nil || c == nil {
			t.Fatalf("claim %s: %+v %v", nodeID, c, err)
		}
		return c.Token
	}
	plan := `{"nodes":[{"id":"a","deps":[],"spec_hash":"sha256:` + strings.Repeat("b", 64) + `"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hs.URL+"/api/v1/runs/run-s/plan", strings.NewReader(plan))
	req.Header.Set("Authorization", "Bearer "+claim(store.PlanNodeID))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("accept plan: %v %v", resp, err)
	}
	_ = resp.Body.Close()
	return segmentController{st: st, url: hs.URL}, claim("a")
}

func (c segmentController) log(t *testing.T) *logseg.Log {
	t.Helper()
	return c.nodeLog(t, "run-s", "a")
}

func (c segmentController) nodeLog(t *testing.T, runID, nodeID string) *logseg.Log {
	t.Helper()
	rec, err := c.st.LatestNodeLog(t.Context(), store.DefaultTeam, runID, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := fs.NewArtifactStore(c.st.OutputDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := logseg.Open(t.Context(), objs, rec.Prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Read(t.Context(), objs, storage.ReadOpts{}); err != nil {
		t.Fatal(err)
	}
	return l
}

// A launched node's pipeline child posts its log to the launcher process
// over loopback with its claim token, as it would to a logs service; the
// launcher seals the log once the child exits.
func TestLaunchedNodeLogReachesTheControllerAsSegments(t *testing.T) {
	ctrl, tok := newSegmentController(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := openNodeLogSink(t.Context(), client.NewWithToken(ctrl.url, nil, tok), "run-s", "a", logger)
	if sink == nil {
		t.Fatal("the controller issued no segment URLs")
	}
	url, stop, err := serveLoopbackLogs(sink, tok, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t.Context())

	stranger, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/api/v1/logs/run-s/a", strings.NewReader("x\n"))
	stranger.Header.Set("Authorization", "Bearer swc_someone_else")
	if resp, err := http.DefaultClient.Do(stranger); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a request without the node's token: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	nodeLog, err := NewHTTPLogsWithToken(url, nil, tok, logger).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"one", "two", "three"} {
		nodeLog.Log("info", msg)
	}
	if err := nodeLog.Close(); err != nil {
		t.Fatal(err)
	}
	sink.close(t.Context(), false)

	l := ctrl.log(t)
	if l.Manifest == nil || l.Manifest.Status != logseg.StatusComplete || l.Manifest.Lines != 3 || l.Broken != 0 {
		t.Fatalf("manifest = %+v", l.Manifest)
	}
}

func TestNodeLogStaysLocalOnAControllerWithoutSegments(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unsupported","route":"POST /api/v1/runs/{id}/nodes/{nodeID}/log-segments"}`))
	}))
	defer old.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if sink := openNodeLogSink(t.Context(), client.NewWithToken(old.URL, nil, "tok"), "run-s", "a", logger); sink != nil {
		t.Fatal("a controller without the route was handed the node's log")
	}
}

func TestHeartbeatWatchReportsTurnTheLiveViewOnAndOff(t *testing.T) {
	srv, pushes := newFakeSegmentController(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := openNodeLogSink(t.Context(), client.New(srv.URL, nil), "r", "n", logger)
	if sink == nil {
		t.Fatal("no sink")
	}
	appendLine := func(line string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/r/n", strings.NewReader(line))
		rec := httptest.NewRecorder()
		sink.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("append %q: %d", line, rec.Code)
		}
	}
	appendLine("one\n")
	NoteWatch(client.WatchReport{RunID: "r", WholeRun: true, Nodes: []string{"n"}})
	appendLine("two\n")
	NoteWatch(client.WatchReport{RunID: "r", NodeID: "n", Watched: false})
	appendLine("three\n")
	sink.close(t.Context(), false)
	close(pushes)
	var got strings.Builder
	for p := range pushes {
		if p.off != strconv.Itoa(got.Len()) {
			t.Fatalf("push at offset %s after %d bytes", p.off, got.Len())
		}
		got.WriteString(p.body)
	}
	if got.String() != "one\ntwo\n" {
		t.Fatalf("live pushes = %q, want lines one and two and nothing after the viewer left", got.String())
	}
}

type livePush struct {
	off  string
	body string
}

func newFakeSegmentController(t *testing.T) (*httptest.Server, chan livePush) {
	t.Helper()
	pushes := make(chan livePush, 64)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/runs/r/nodes/n/log-segments":
			var req store.LogSegmentRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			grant := store.LogSegmentGrant{Attempt: 1, Generation: 1, Prefix: "p/"}
			if req.N > 0 {
				grant.URL = srv.URL + "/put/" + strconv.Itoa(req.N)
			}
			_ = json.NewEncoder(w).Encode(grant)
		case r.URL.Path == "/api/v1/runs/r/nodes/n/log-manifest":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/put/"):
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v1/runs/r/nodes/n/logs":
			body, _ := io.ReadAll(r.Body)
			pushes <- livePush{off: r.Header.Get(store.LiveLogOffsetHeader), body: string(body)}
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/runs/r/heartbeat" && r.Header.Get(store.LogWatchHeader) == "1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"watched_nodes":["n"]}`))
		case r.URL.Path == "/api/v1/runs/r/heartbeat":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, pushes
}
