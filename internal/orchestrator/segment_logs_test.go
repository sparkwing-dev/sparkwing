package orchestrator

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
		if err := sink.sink.Append([]byte(line), "", 0, 0); err != nil {
			t.Fatalf("append %q: %v", line, err)
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
