package orchestrator

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
