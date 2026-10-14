package orchestrator

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
)

func TestLoopbackLogsOneLineForANodeSpeakingAnotherProtocol(t *testing.T) {
	var logged bytes.Buffer
	h := (&nodeProtocolCheck{logger: slog.New(slog.NewTextHandler(&logged, nil))}).wrap(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	get := func(version string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1", nil)
		if version != "" {
			req.Header.Set(authwire.NodeProtocolHeader, version)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("version %q: status %d, want the request served", version, rec.Code)
		}
	}
	get(authwire.NodeProtocolVersion)
	if logged.Len() != 0 {
		t.Fatalf("a matching version logged: %s", logged.String())
	}
	get("0")
	get("")
	if got := strings.Count(logged.String(), "node protocol version mismatch"); got != 1 {
		t.Fatalf("mismatch lines = %d, want 1: %s", got, logged.String())
	}
	if !strings.Contains(logged.String(), "got=0") {
		t.Fatalf("mismatch line does not name the version the node sent: %s", logged.String())
	}
}
