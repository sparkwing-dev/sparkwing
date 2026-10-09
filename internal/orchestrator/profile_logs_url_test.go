package orchestrator

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// A controller profile's logs go to that controller as segments, so the
// spec names the controller and nothing is asked of it to build the spec.
func TestProfileSurfaceSpecs_LogsNameTheController(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: srv.URL},
	}
	state, logs, cache := profileSurfaceSpecs(p, "/tmp/state.db")

	if logs == nil || logs.Type != backends.TypeController || logs.Controller != "prod" || logs.URL != "" {
		t.Fatalf("logs spec = %#v, want the profile's controller with no URL of its own", logs)
	}
	if state.URL != "" || cache.URL != "" {
		t.Errorf("state/cache URLs = %q/%q, want empty (they resolve through the controller)", state.URL, cache.URL)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("building the specs sent %d request(s) to the controller", got)
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
