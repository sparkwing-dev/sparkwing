package localws

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMuxSpecificity_ApiV1Routing pins the assumption that Go 1.22's
// ServeMux picks the most specific pattern (not first-registered, not
// longest-prefix). If this fails, dashboard /api/v1 routes silently
// fall through to the controller's catch-all and the dashboard goes
// dark.
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
		// Run detail + cancel + paused fall through to the controller.
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

// TestMuxSpecificity_S3OnlyMode pins conditional routing in S3-only
// mode: with no controller catch-all, /api/v1/runs and
// /api/v1/runs/{id} must land on dashboard handlers.
func TestMuxSpecificity_S3OnlyMode(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/runs/{id}/logs", marker("logs"))
	mux.Handle("GET /api/v1/runs/{id}/events/stream", marker("events-stream"))
	mux.Handle("GET /api/v1/capabilities", marker("capabilities"))
	mux.Handle("GET /api/v1/runs", marker("list-runs"))
	mux.Handle("GET /api/v1/runs/{id}", marker("get-run"))
	// No controller catch-all in s3-only mode.
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
		// Mutating routes have no handler in s3-only mode; they fall
		// through to the SPA catch-all.
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
