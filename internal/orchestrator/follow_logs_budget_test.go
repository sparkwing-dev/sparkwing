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

func shortFollowBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := remoteFollowFailureBudget
	remoteFollowFailureBudget = d
	t.Cleanup(func() { remoteFollowFailureBudget = prev })
}

// followSpy answers the two reads the follow loop makes. getRun is
// consulted per call so a test can flip the controller from healthy to
// dead mid-follow.
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

// A controller that dies mid-follow used to leave the follow loop
// polling for as long as the process lived, because every GetRun error
// was discarded and re-polled. `pipeline trigger` then hung instead of
// reaching the exit-3 unknown-outcome path that exists for exactly
// this. The follow now gives up once the status has been unreadable
// for the whole budget and hands the transport error back.
func TestFollowLogsRemote_GivesUpOnADeadController(t *testing.T) {
	shortFollowBudget(t, time.Second)
	const runID = "run-dead-controller"
	url := followSpy(t, runID, func(n int32) (store.Run, bool) {
		// One healthy poll, then the controller is gone for good.
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

// A blip is not a death. Errors that are interrupted by a successful
// status read must not accumulate toward the budget, or a controller
// replica rolling out mid-run would abort a follow that is working
// fine.
func TestFollowLogsRemote_SuccessfulPollResetsTheBudget(t *testing.T) {
	shortFollowBudget(t, time.Second)
	const runID = "run-blippy-controller"
	url := followSpy(t, runID, func(n int32) (store.Run, bool) {
		switch {
		case n <= 2: // first burst of failures
			return store.Run{}, false
		case n == 3: // the reset
			return runningRun(runID), true
		case n <= 5: // second burst, would exceed the budget if they summed
			return store.Run{}, false
		}
		now := time.Now()
		return store.Run{
			ID: runID, Pipeline: "release", Status: "success",
			StartedAt: now.Add(-time.Second), FinishedAt: &now,
		}, true
	})

	ctrl := client.NewWithToken(url, nil, "")
	logc := sparkwinglogs.New(url, nil, "")

	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- followLogsRemote(context.Background(), ctrl, logc, runID, "", io.Discard) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("follow aborted on interrupted blips: %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
			t.Fatalf("recovered follow returned after %s, want under 500ms", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("follow never returned")
	}
}

// Cancelling the caller's context also fails the status read; that is
// the operator leaving, and it must not be reported as a controller
// failure.
func TestFollowLogsRemote_CancelIsNotATransportFailure(t *testing.T) {
	shortFollowBudget(t, time.Millisecond)
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
