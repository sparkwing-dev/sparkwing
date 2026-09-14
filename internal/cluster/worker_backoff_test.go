package cluster

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TestRunWorker_NamesItselfAndHonorsARefusal covers the worker loop against a
// controller running a limits profile: it has to carry an identity of its own
// or share one bucket with every other worker on the token, and it has to wait
// out the Retry-After rather than repolling at its own cadence and logging an
// error line each time.
func TestRunWorker_NamesItselfAndHonorsARefusal(t *testing.T) {
	var mu sync.Mutex
	var identities []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/triggers/claim" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mu.Lock()
		identities = append(identities, r.Header.Get(store.RunnerIdentityHeader))
		mu.Unlock()
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	var logged strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := RunWorker(ctx, orchestrator.WorkerOptions{
		ControllerURL: ts.URL,
		Paths:         orchestrator.Paths{Root: t.TempDir()},
		PollInterval:  time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("RunWorker: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(identities) == 0 {
		t.Fatal("the worker never claimed")
	}
	want := logs.ProcessIdentity("worker")
	for _, got := range identities {
		if got != want {
			t.Fatalf("claim sent %s=%q, want %q", store.RunnerIdentityHeader, got, want)
		}
	}
	if len(identities) > 2 {
		t.Errorf("the worker polled %d times through a 30s Retry-After", len(identities))
	}
	out := logged.String()
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("a shed claim logged an error line:\n%s", out)
	}
	if got := strings.Count(out, "controller is shedding claims"); got != 1 {
		t.Fatalf("shed claims logged %d notices, want 1 per window:\n%s", got, out)
	}
}
