package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type workerController struct {
	*httptest.Server
	beat     chan struct{}
	mu       sync.Mutex
	finished []string
}

func newWorkerController(t *testing.T) *workerController {
	t.Helper()
	c := &workerController{beat: make(chan struct{}, 64)}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			select {
			case c.beat <- struct{}{}:
			default:
			}
			_, _ = w.Write([]byte(`{"cancel_requested":false}`))
		case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/finish") || strings.HasSuffix(r.URL.Path, "/done")):
			c.mu.Lock()
			c.finished = append(c.finished, r.URL.Path)
			c.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func stubWorkerHost(t *testing.T, build func(ctx context.Context) error) {
	t.Helper()
	prevBuild, prevHost := buildCheckoutPipeline, hostTriggerRun
	t.Cleanup(func() { buildCheckoutPipeline, hostTriggerRun = prevBuild, prevHost })
	buildCheckoutPipeline = func(ctx context.Context) (string, string, func(), error) {
		return "pipeline", "dir", func() {}, build(ctx)
	}
	hostTriggerRun = func(context.Context, orchestrator.TriggerHost) error { return nil }
}

// safety: the worker renews the trigger claim while it builds the pipeline,
// so a cold build longer than the claim's lease keeps the claim.
func TestWorkerRenewsTheClaimWhileItBuilds(t *testing.T) {
	ctrl := newWorkerController(t)
	stubWorkerHost(t, func(ctx context.Context) error {
		select {
		case <-ctrl.beat:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	trig := &store.Trigger{ID: "run-built", Pipeline: "p"}
	if err := hostTrigger(ctx, client.New(ctrl.URL, nil), trig, ctrl.URL, "token", 100*time.Millisecond); err != nil {
		t.Fatalf("the build never saw a heartbeat: %v", err)
	}
}

// safety: a worker shutting down mid-build leaves the trigger and its run to
// the claim's lease rather than fail them.
func TestWorkerShutdownMidBuildLeavesTheTriggerToItsLease(t *testing.T) {
	ctrl := newWorkerController(t)
	building := make(chan struct{})
	stubWorkerHost(t, func(ctx context.Context) error {
		close(building)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-building
		cancel()
	}()
	trig := &store.Trigger{ID: "run-stopped", Pipeline: "p"}
	_ = hostTrigger(ctx, client.New(ctrl.URL, nil), trig, ctrl.URL, "token", time.Hour)
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.finished) != 0 {
		t.Fatalf("a worker shutting down finished %v, want the trigger left to its lease", ctrl.finished)
	}
}
