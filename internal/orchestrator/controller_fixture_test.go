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
