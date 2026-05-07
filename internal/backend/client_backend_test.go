package backend_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestClientBackend_Capabilities(t *testing.T) {
	t.Parallel()
	b := backend.NewClientBackend(client.New("http://example.invalid", nil), nil)
	got, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got.Mode != "cluster" {
		t.Errorf("Mode = %q, want cluster", got.Mode)
	}
	if got.ReadOnly {
		t.Errorf("ReadOnly = true, want false")
	}
	if got.Storage.Runs != "controller" {
		t.Errorf("Storage.Runs = %q, want controller", got.Storage.Runs)
	}
}

// fakeController stands in for the cluster controller. Only routes
// ClientBackend touches are wired.
func fakeController(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(store.Run{ID: "r1", Pipeline: "p", Status: "running"})
	})
	mux.HandleFunc("/api/v1/runs/r1/cancel", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/runs/r1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nodes": []store.Node{{RunID: "r1", NodeID: "n1", Status: "completed"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClientBackend_RunsAndCancel(t *testing.T) {
	t.Parallel()
	srv := fakeController(t)
	b := backend.NewClientBackend(client.New(srv.URL, nil), nil)
	ctx := context.Background()

	got, err := b.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.ID != "r1" {
		t.Errorf("GetRun.ID = %q", got.ID)
	}
	nodes, err := b.ListNodes(ctx, "r1")
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("ListNodes len = %d, want 1", len(nodes))
	}
}

func TestClientBackend_NilLogStoreReturnsEmpty(t *testing.T) {
	t.Parallel()
	b := backend.NewClientBackend(client.New("http://example.invalid", nil), nil)
	got, err := b.ReadNodeLog(context.Background(), "r1", "n1", backend.ReadOpts{})
	if err != nil {
		t.Fatalf("ReadNodeLog: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadNodeLog = %q, want empty", got)
	}
	stream, err := b.StreamNodeLog(context.Background(), "r1", "n1")
	if err != nil {
		t.Fatalf("StreamNodeLog: %v", err)
	}
	if stream != nil {
		stream.Close()
		t.Errorf("StreamNodeLog = non-nil, want nil")
	}
}
