package orchestrator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
)

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

func newHeartbeatServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/triggers/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// withFastSilence shrinks the silence/timeout knobs. Interval is
// passed directly into runHeartbeat so no override needed.
func withFastSilence(t *testing.T, timeout, silence time.Duration) {
	t.Helper()
	oldTimeout := runHeartbeatTimeout
	oldSilence := runHeartbeatMaxSilence
	runHeartbeatTimeout = timeout
	runHeartbeatMaxSilence = silence
	t.Cleanup(func() {
		runHeartbeatTimeout = oldTimeout
		runHeartbeatMaxSilence = oldSilence
	})
}

// TestRunHeartbeat_ReapedCancelsRun: controller returns 404 on the
// first heartbeat (trigger was reaped / no longer claimed). The
// heartbeat must cancel the run ctx and NOT set the cancelled flag
// (reaped != operator-cancel; the controller's reaper will mark
// the run 'failed' with runner-lease-expired reason).
func TestRunHeartbeat_ReapedCancelsRun(t *testing.T) {
	withFastSilence(t, 50*time.Millisecond, time.Second)

	ts := newHeartbeatServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	cli := client.New(ts.URL, nil)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	cancelled := &atomic.Bool{}
	done := make(chan struct{})
	go func() {
		runHeartbeat(context.Background(), cli, "trig-x",
			5*time.Millisecond, cancelRun, cancelled, discardLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runHeartbeat did not return after 404")
	}
	if runCtx.Err() == nil {
		t.Error("run ctx was not cancelled on reap")
	}
	if cancelled.Load() {
		t.Error("cancelled flag set on reap (should stay false so run is marked failed, not cancelled)")
	}
}

// TestRunHeartbeat_SilenceCancelsRun: controller returns 500
// repeatedly. After silence window elapses, run ctx must be
// cancelled and cancelled flag stays false.
func TestRunHeartbeat_SilenceCancelsRun(t *testing.T) {
	withFastSilence(t, 20*time.Millisecond, 100*time.Millisecond)

	ts := newHeartbeatServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	cli := client.New(ts.URL, nil)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	cancelled := &atomic.Bool{}
	done := make(chan struct{})
	go func() {
		runHeartbeat(context.Background(), cli, "trig-x",
			10*time.Millisecond, cancelRun, cancelled, discardLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("runHeartbeat did not return after silence window")
	}
	if runCtx.Err() == nil {
		t.Error("run ctx was not cancelled after silence window")
	}
	if cancelled.Load() {
		t.Error("cancelled flag set on silence (should stay false)")
	}
}

// TestRunHeartbeat_OperatorCancel: controller returns 200 with
// cancel_requested=true. Run ctx must be cancelled AND cancelled
// flag must be set so the caller marks the run as 'cancelled'.
func TestRunHeartbeat_OperatorCancel(t *testing.T) {
	ts := newHeartbeatServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cancel_requested":true}`))
	})
	cli := client.New(ts.URL, nil)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	cancelled := &atomic.Bool{}
	hbCtx, stopHB := context.WithCancel(context.Background())
	defer stopHB()
	done := make(chan struct{})
	go func() {
		runHeartbeat(hbCtx, cli, "trig-x",
			5*time.Millisecond, cancelRun, cancelled, discardLogger())
		close(done)
	}()

	// Wait for the cancel flag to be observed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cancelled.Load() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	stopHB()
	<-done

	if runCtx.Err() == nil {
		t.Error("run ctx was not cancelled on operator cancel")
	}
	if !cancelled.Load() {
		t.Error("cancelled flag not set on operator cancel")
	}
}
