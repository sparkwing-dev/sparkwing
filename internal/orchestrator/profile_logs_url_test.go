package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func TestProfileSurfaceSpecs_LogsCarryTheAnnouncedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/services" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"logs":"http://logs.example.dev"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: srv.URL},
	}
	state, logs, cache := profileSurfaceSpecs(p, "/tmp/state.db")

	if logs == nil || logs.Type != backends.TypeController {
		t.Fatalf("logs spec = %#v, want a controller-typed spec", logs)
	}
	if logs.URL != "http://logs.example.dev" {
		t.Errorf("logs URL = %q, want the announced logs service", logs.URL)
	}

	if state.URL != "" || cache.URL != "" {
		t.Errorf("state/cache URLs = %q/%q, want empty (they resolve through the controller)", state.URL, cache.URL)
	}
}

func TestProfileSurfaceSpecs_NoAnnouncementLeavesTheFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no services announced", http.StatusNotFound)
	}))
	defer srv.Close()

	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: srv.URL},
	}
	_, logs, _ := profileSurfaceSpecs(p, "/tmp/state.db")
	if logs == nil || logs.URL != "" {
		t.Errorf("logs URL = %#v, want empty so the controller URL stays the fallback", logs)
	}
}

func TestProfileSurfaceSpecs_ExplicitLogsSurfaceWins(t *testing.T) {
	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: "http://controller.example.dev"},
		State:      &backends.Spec{Type: backends.TypeSQLite, Path: "/tmp/x.db"},
		Logs:       &backends.Spec{Type: backends.TypeS3, Bucket: "b", Prefix: "logs"},
		Cache:      &backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/c"},
	}
	_, logs, _ := profileSurfaceSpecs(p, "/tmp/state.db")
	if logs.Type != backends.TypeS3 || logs.Bucket != "b" {
		t.Errorf("logs spec = %#v, want the profile's own s3 surface", logs)
	}
}
