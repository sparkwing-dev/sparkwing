package orchestrator

import (
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func NewControllerServer(t *testing.T, st *store.Store, ctrlLogger *slog.Logger) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(controller.New(st, ctrlLogger).Handler())
	t.Cleanup(srv.Close)
	return srv
}
