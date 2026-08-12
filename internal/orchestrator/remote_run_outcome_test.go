package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// outcomeController stands up a real controller.Server over an
// in-memory-ish SQLite store and seeds one terminal run so
// RemoteRunOutcome reads a run the same way the CLI does.
func outcomeController(t *testing.T, status, runErr string) string {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatalf("controller store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.CreateRun(ctx, store.Run{
		ID: "run-outcome", Pipeline: "release", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-outcome", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.StartNode(ctx, "run-outcome", "build"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}
	nodeOutcome, nodeErr := "success", ""
	if status != "success" {
		nodeOutcome, nodeErr = "failed", "go build: undefined: Frobnicate"
	}
	if err := st.FinishNode(ctx, "run-outcome", "build", nodeOutcome, nodeErr, nil); err != nil {
		t.Fatalf("FinishNode: %v", err)
	}
	if err := st.FinishRun(ctx, "run-outcome", status, runErr); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRemoteRunOutcome_ReportsTerminalStatus is the contract the
// `pipeline trigger` exit code rests on: the follow ends carrying no
// outcome, so this read is what tells the CLI whether the remote run
// succeeded, and a non-success run must leave its failure detail on
// the operator's terminal.
func TestRemoteRunOutcome_ReportsTerminalStatus(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		runErr      string
		wantSummary bool
	}{
		{name: "success", status: "success", wantSummary: false},
		{name: "failed", status: "failed", runErr: "node build failed", wantSummary: true},
		{name: "cancelled", status: "cancelled", runErr: "cancelled by operator", wantSummary: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := outcomeController(t, tc.status, tc.runErr)
			var buf bytes.Buffer
			got, err := orchestrator.RemoteRunOutcome(context.Background(), url, "", "run-outcome", &buf)
			if err != nil {
				t.Fatalf("RemoteRunOutcome: %v", err)
			}
			if got != tc.status {
				t.Errorf("status = %q, want %q", got, tc.status)
			}
			out := buf.String()
			if !tc.wantSummary {
				if strings.TrimSpace(out) != "" {
					t.Errorf("a successful run should print no failure summary; got:\n%s", out)
				}
				return
			}
			for _, want := range []string{"run-outcome", "status:    " + tc.status, tc.runErr, "undefined: Frobnicate"} {
				if !strings.Contains(out, want) {
					t.Errorf("summary missing %q; got:\n%s", want, out)
				}
			}
		})
	}
}

// TestRemoteRunOutcome_UnreadableStatusIsAnError keeps a controller
// that cannot answer from being reported as either outcome: the
// caller has to be able to tell "unknown" apart from "failed".
func TestRemoteRunOutcome_UnreadableStatusIsAnError(t *testing.T) {
	url := outcomeController(t, "success", "")
	var buf bytes.Buffer
	status, err := orchestrator.RemoteRunOutcome(context.Background(), url, "", "run-missing", &buf)
	if err == nil {
		t.Fatalf("expected an error for an unknown run; status = %q", status)
	}
	if status != "" {
		t.Errorf("status = %q, want empty when the read failed", status)
	}
}

// TestRemoteRunOutcome_RetriesOneTransportBlip guards the difference
// between "the controller hiccuped" and "the outcome is unknown". The
// follow just watched this run go terminal, so a single 503 from a
// controller replica rolling out must not be promoted into an
// unknown-outcome verdict for the caller.
func TestRemoteRunOutcome_RetriesOneTransportBlip(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case r.URL.Path == "/api/v1/runs/run-blip":
			if calls.Add(1) == 1 {
				http.Error(w, "controller unavailable", http.StatusServiceUnavailable)
				return
			}
			now := time.Now()
			_ = json.NewEncoder(w).Encode(store.Run{
				ID: "run-blip", Pipeline: "release", Status: "failed", Error: "node build failed",
				StartedAt: now.Add(-time.Second), FinishedAt: &now,
			})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	status, err := orchestrator.RemoteRunOutcome(context.Background(), srv.URL, "", "run-blip", &buf)
	if err != nil {
		t.Fatalf("RemoteRunOutcome: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want failed (the retry should have read it)", status)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("GetRun calls = %d, want 2 (one blip, one retry)", got)
	}
	if !strings.Contains(buf.String(), "node build failed") {
		t.Errorf("summary missing the run error; got:\n%s", buf.String())
	}
}

// TestRemoteRunOutcome_RequiresControllerURL matches the guard every
// other *Remote entry point in this file carries.
func TestRemoteRunOutcome_RequiresControllerURL(t *testing.T) {
	if _, err := orchestrator.RemoteRunOutcome(context.Background(), "", "", "run-x", nil); err == nil {
		t.Fatal("expected an error when no controller URL is configured")
	}
}
