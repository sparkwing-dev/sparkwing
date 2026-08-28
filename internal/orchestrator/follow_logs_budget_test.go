package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func shortFollowTiming(t *testing.T, budget, interval time.Duration) {
	t.Helper()
	prevBudget := remoteFollowFailureBudget
	prevInterval := remoteFollowPollInterval
	remoteFollowFailureBudget = budget
	remoteFollowPollInterval = interval
	t.Cleanup(func() {
		remoteFollowFailureBudget = prevBudget
		remoteFollowPollInterval = prevInterval
	})
}

func followSpy(t *testing.T, runID string, getRun func(n int32) (store.Run, bool)) string {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case r.URL.Path == "/api/v1/runs/"+runID:
			run, ok := getRun(calls.Add(1))
			if !ok {
				http.Error(w, "controller unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(run)
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func followSpyWithStatusRead(t *testing.T, runID string, getRun func(n int32) (store.Run, bool)) (string, <-chan struct{}) {
	t.Helper()
	statusRead := make(chan struct{}, 1)
	url := followSpy(t, runID, func(n int32) (store.Run, bool) {
		select {
		case statusRead <- struct{}{}:
		default:
		}
		return getRun(n)
	})
	return url, statusRead
}

func runningRun(runID string) store.Run {
	return store.Run{ID: runID, Pipeline: "release", Status: "running", StartedAt: time.Now().Add(-time.Second)}
}

func TestFollowLogsRemote_NoStreamsReturnsWithoutDrainDelay(t *testing.T) {
	const runID = "run-no-streams"
	terminalRead := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case r.URL.Path == "/api/v1/runs/"+runID:
			now := time.Now()
			_ = json.NewEncoder(w).Encode(store.Run{
				ID: runID, Pipeline: "release", Status: "success",
				StartedAt: now.Add(-time.Second), FinishedAt: &now,
			})
			close(terminalRead)
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(srv.Close)

	done := make(chan error, 1)
	go func() {
		done <- followLogsRemote(
			context.Background(),
			client.NewWithToken(srv.URL, nil, ""),
			sparkwinglogs.New(srv.URL, nil, ""),
			runID, "", io.Discard,
		)
	}()

	select {
	case <-terminalRead:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal status was never read")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow terminal run: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("follow delayed after finding no streams to drain")
	}
}

func TestFollowLogsRemote_GivesUpOnADeadController(t *testing.T) {
	shortFollowTiming(t, 60*time.Millisecond, 10*time.Millisecond)
	const runID = "run-dead-controller"
	url := followSpy(t, runID, func(n int32) (store.Run, bool) {
		if n == 1 {
			return runningRun(runID), true
		}
		return store.Run{}, false
	})

	ctrl := client.NewWithToken(url, nil, "")
	logc := sparkwinglogs.New(url, nil, "")

	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- followLogsRemote(context.Background(), ctrl, logc, runID, "", io.Discard) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("follow returned nil against a controller that never answered again; " +
				"remoteFollowExit needs the transport error to reach exit 3")
		}
		if !strings.Contains(err.Error(), runID) {
			t.Errorf("error = %v, want the run named", err)
		}
		if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
			t.Fatalf("dead-controller follow returned after %s, want under 500ms", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("follow never returned: the poll loop is still unbounded")
	}
}

func TestFollowLogsRemote_SuccessfulPollResetsTheBudget(t *testing.T) {
	start := time.Unix(100, 0)
	var failures remoteFollowFailures
	if _, exhausted := failures.failed(start, time.Minute); exhausted {
		t.Fatal("first failure exhausted the budget")
	}
	if _, exhausted := failures.failed(start.Add(50*time.Second), time.Minute); exhausted {
		t.Fatal("first failure burst exhausted the budget")
	}
	failures.succeeded()
	if _, exhausted := failures.failed(start.Add(55*time.Second), time.Minute); exhausted {
		t.Fatal("first failure after recovery exhausted the reset budget")
	}
	if _, exhausted := failures.failed(start.Add(65*time.Second), time.Minute); exhausted {
		t.Fatal("interrupted failure bursts accumulated across a success")
	}
}

func TestFollowLogsRemote_CancelIsNotATransportFailure(t *testing.T) {
	shortFollowTiming(t, time.Millisecond, 10*time.Millisecond)
	const runID = "run-cancelled-follow"
	url, statusRead := followSpyWithStatusRead(t, runID, func(int32) (store.Run, bool) { return runningRun(runID), true })

	ctrl := client.NewWithToken(url, nil, "")
	logc := sparkwinglogs.New(url, nil, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- followLogsRemote(ctx, ctrl, logc, runID, "", io.Discard) }()
	select {
	case <-statusRead:
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not read run status")
	}
	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled follow reported a transport failure: %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
			t.Fatalf("cancelled follow returned after %s, want under 500ms", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("follow never returned after cancel")
	}
}
