package orchestrator

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// NewControllerServer serves a controller with the log-append endpoint
// mounted alongside it, the way a real deployment does (see
// pkg/localws, which mounts both on one mux).
//
// A controller-only handler 404s every log append, and a run whose log
// lines were all lost now fails rather than reporting success -- so a
// fixture without logs tests the failure path of whatever it was
// actually about.
//
// Exported so the external orchestrator_test package can use the same
// fixture; it is declared in a _test.go file, so it ships with no
// binary.
func NewControllerServer(t *testing.T, st *store.Store, ctrlLogger *slog.Logger) *httptest.Server {
	t.Helper()
	logsSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("logs server: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/logs/", logsSrv.Handler())
	mux.Handle("/", controller.New(st, ctrlLogger).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
