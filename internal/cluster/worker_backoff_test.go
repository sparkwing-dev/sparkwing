package cluster

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the loop is done with a refused claim once it has said so, so the
// test watches the log rather than waiting a wall-clock interval out.
type awaitedLog struct {
	mu   sync.Mutex
	out  strings.Builder
	want string
	once sync.Once
	seen chan struct{}
}

func newAwaitedLog(want string) *awaitedLog {
	return &awaitedLog{want: want, seen: make(chan struct{})}
}

func (l *awaitedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out.Write(p)
	if strings.Contains(l.out.String(), l.want) {
		l.once.Do(func() { close(l.seen) })
	}
	return len(p), nil
}

func (l *awaitedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.String()
}

// TestRunWorker_NamesItselfAndHonorsARefusal covers the worker loop against a
// controller running a limits profile: it has to carry an identity of its own
// or share one bucket with every other worker on the token, and it has to wait
// the Retry-After out rather than repolling at its own cadence and logging an
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

	logged := newAwaitedLog("controller is shedding claims")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-logged.seen
		cancel()
	}()

	err := RunWorker(ctx, orchestrator.WorkerOptions{
		ControllerURL: ts.URL,
		Paths:         orchestrator.Paths{Root: t.TempDir()},
		Logger:        slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
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
	out := logged.String()
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("a shed claim logged an error line:\n%s", out)
	}
	if got := strings.Count(out, "controller is shedding claims"); got != 1 {
		t.Fatalf("shed claims logged %d notices, want 1 per window:\n%s", got, out)
	}
	if !strings.Contains(out, "retry_after=") {
		t.Fatalf("the notice does not name the wait:\n%s", out)
	}
}
