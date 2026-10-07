package orchestrator

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestBrokerLogsOneLineForANodeSpeakingAnotherProtocol(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	var logged bytes.Buffer
	broker, err := startRemoteExecutionBroker(upstream.URL, "", "parent-token", "run-1", "node-a", store.NodeClaimFence{}, nil,
		slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()

	get := func(version string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, broker.URL()+"/api/v1/runs/run-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+broker.capability)
		if version != "" {
			req.Header.Set(authwire.NodeProtocolHeader, version)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("version %q: status %d, want the request served", version, resp.StatusCode)
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
