package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
)

func TestDiscoverLogsURL(t *testing.T) {
	cases := []struct {
		name     string
		services string
		want     string
		wantErr  string
	}{
		{name: "announced", services: `{"logs":"http://logs.internal:8081"}`, want: "http://logs.internal:8081"},
		{name: "not announced", services: `{"cache_pod":"http://cache.internal"}`, wantErr: "announces no logs service"},
		{name: "no services route", wantErr: "announces no logs service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logAppends int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/api/v1/logs/"):
					logAppends++
					http.NotFound(w, r)
				case r.URL.Path == "/api/v1/services" && tc.services != "":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.services))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			// safety: discovery caches by URL for the process, and a reused test port would return another test's services.
			discovery.ResetCache()
			t.Cleanup(discovery.ResetCache)

			got, err := discoverLogsURL(t.Context(), srv.URL, "")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), srv.URL) {
					t.Fatalf("err = %v, want one naming %s and %q", err, srv.URL, tc.wantErr)
				}
				if got != "" {
					t.Fatalf("url = %q alongside the error", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("discoverLogsURL = %q, %v; want %q", got, err, tc.want)
			}
			if logAppends != 0 {
				t.Fatalf("the controller received %d log request(s)", logAppends)
			}
		})
	}
}
