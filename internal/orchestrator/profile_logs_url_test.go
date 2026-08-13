package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// The laptop path -- `sparkwing run --profile prod` against a
// controller -- does not go through RemoteBackends. It builds its
// surfaces here, and a controller-only profile got a controller-typed
// logs spec that resolved to the controller's own URL, which routes no
// log appends.
//
// A first fix that only covered RemoteBackends passed its unit test and
// still lost every line in a live two-process deployment, so this pins
// the path that actually runs.
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
	// State and cache genuinely do live on the controller, so neither
	// should have been redirected.
	if state.URL != "" || cache.URL != "" {
		t.Errorf("state/cache URLs = %q/%q, want empty (they resolve through the controller)", state.URL, cache.URL)
	}
}

// A controller that announces nothing leaves the URL empty, which
// keeps the historical fallback -- correct for a co-located deployment
// where one process serves both.
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

// A profile that declares its own logs surface is untouched: naming a
// store outright is more specific than anything discovery can say.
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
