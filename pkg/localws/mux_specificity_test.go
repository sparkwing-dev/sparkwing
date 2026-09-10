package localws

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestMuxSpecificity_ApiV1Routing(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/logs", marker("logs"))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}", marker("node-log"))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}/stream", marker("node-stream"))
	mux.Handle("GET /api/v1/runs/{id}/events/stream", marker("events-stream"))
	mux.Handle("GET /api/v1/capabilities", marker("capabilities"))
	mux.Handle("/api/v1/", marker("controller-catchall"))

	cases := []struct {
		path string
		want string
	}{
		{"/api/v1/runs/abc/logs", "logs"},
		{"/api/v1/runs/abc/logs/web-ok", "node-log"},
		{"/api/v1/runs/abc/logs/web-ok/stream", "node-stream"},
		{"/api/v1/runs/abc/events/stream", "events-stream"},
		{"/api/v1/capabilities", "capabilities"},
		{"/api/v1/runs/abc", "controller-catchall"},
		{"/api/v1/runs/abc/cancel", "controller-catchall"},
		{"/api/v1/runs/abc/paused", "controller-catchall"},
		{"/api/v1/runs/abc/nodes/web-ok/release", "controller-catchall"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Marker")
			if got != tc.want {
				t.Fatalf("path %s: got %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestMuxSpecificity_CronRoutes(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/crons", marker("overview"))
	mux.Handle("GET /api/v1/crons/{id}", marker("detail"))
	mux.Handle("POST /api/v1/crons/{id}/pause", marker("pause"))
	mux.Handle("POST /api/v1/crons/{id}/resume", marker("resume"))
	mux.Handle("POST /api/v1/crons/{id}/run", marker("run"))
	mux.Handle("/api/v1/", marker("controller-catchall"))

	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, "/api/v1/crons", "overview"},
		{http.MethodGet, "/api/v1/crons/crn_0123456789ab", "detail"},
		{http.MethodPost, "/api/v1/crons/crn_0123456789ab/pause", "pause"},
		{http.MethodPost, "/api/v1/crons/crn_0123456789ab/resume", "resume"},
		{http.MethodPost, "/api/v1/crons/crn_0123456789ab/run", "run"},
		{http.MethodGet, "/api/v1/crons/dotfiles%2Fnightly", "detail"},
		{http.MethodPost, "/api/v1/crons/dotfiles%2Fnightly/pause", "pause"},
		{http.MethodGet, "/api/v1/crons/crn_0123456789ab/pause", "controller-catchall"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if got := rec.Header().Get("X-Marker"); got != tc.want {
				t.Fatalf("%s %s: got %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestMuxSpecificity_S3OnlyMode(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/logs", marker("logs"))
	mux.Handle("GET /api/v1/runs/{id}/events/stream", marker("events-stream"))
	mux.Handle("GET /api/v1/capabilities", marker("capabilities"))
	mux.Handle("GET /api/v1/runs", marker("list-runs"))
	mux.Handle("GET /api/v1/runs/{id}", marker("get-run"))
	mux.Handle("/", marker("spa"))

	cases := []struct {
		path   string
		want   string
		status int
	}{
		{"/api/v1/runs", "list-runs", http.StatusOK},
		{"/api/v1/runs/abc", "get-run", http.StatusOK},
		{"/api/v1/runs/abc/logs", "logs", http.StatusOK},
		{"/api/v1/runs/abc/events/stream", "events-stream", http.StatusOK},
		{"/api/v1/capabilities", "capabilities", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Marker")
			if got != tc.want {
				t.Fatalf("path %s: got %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func marker(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Marker", name)
		w.WriteHeader(http.StatusOK)
	})
}

// TestMuxSpecificity_DocsReachesTheDocsHandler pins /docs against the real root
// mux. Nothing on that mux names /docs today, so the route survives only because
// the dashboard handler under "/" claims it; a route added above the catch-all
// would take it silently, and the answer would still be a 200.
func TestMuxSpecificity_DocsReachesTheDocsHandler(t *testing.T) {
	if len(docs.List()) == 0 {
		t.Fatal("the embedded doc set is empty, so this test would pass without reading a page")
	}

	paths, err := localPaths(t.TempDir())
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	if err := paths.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	const shellMarker = "stub dashboard shell"
	bundle := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html><body>" + shellMarker + "</body></html>")},
	}
	handler := buildHandler(ctx, cancel, Options{Addr: "127.0.0.1:4343", Version: "v1.2.3"}, handlerParts{
		paths:   paths,
		backend: backend.NewStoreBackend(st, paths, nil),
		store:   st,
	}, bundle)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, path := range []string{"/docs", "/docs/", "/docs?p=" + docs.List()[0].Slug} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if strings.Contains(string(body), shellMarker) {
				t.Fatalf("%s reached the dashboard shell instead of the docs handler", path)
			}
			if !strings.Contains(string(body), "sparkwing docs") {
				t.Fatalf("%s served neither the docs pages nor the shell: %.200s", path, body)
			}
		})
	}
}
