package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunNodeRemoteCancelAbortsSourceFetch(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())

	var once sync.Once
	reached := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reached) })
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-reached
		cancel()
	}()

	trigger := &store.Trigger{RepoURL: "git@github.com:sparkwing-dev/sparkwing.git", GitBranch: "main"}
	run := &store.Run{Pipeline: "demo"}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	done := make(chan error, 1)
	go func() {
		_, err := runNodeRemote(ctx, trigger, run, "", "", srv.URL, "", "run-1", "node-1", "", logger)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("runNodeRemote error = %v, want a cancellation of the source fetch", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runNodeRemote kept fetching source after its context was cancelled")
	}
}
