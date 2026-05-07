package cluster

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
)

// newTriggerHeartbeatServer returns an httptest.Server wired to a
// single handler for POST /api/v1/triggers/*/heartbeat. The handler
// is swapped between tests via the returned atomic pointer so each
// subtest can program its own sequence without restarting the
// server. handlerCalls counts only heartbeat requests.
func newTriggerHeartbeatServer(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int64) {
	t.Helper()
	var handler atomic.Value
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/triggers/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		h, _ := handler.Load().(http.HandlerFunc)
		if h == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &handler, &calls
}

// withFastTriggerHeartbeat shrinks the trigger heartbeat timing knobs
// so silence / reaped paths fire in milliseconds. Restores on cleanup.
func withFastTriggerHeartbeat(t *testing.T, interval, timeout, silence time.Duration) {
	t.Helper()
	oldInterval := triggerHeartbeatInterval
	oldTimeout := triggerHeartbeatTimeout
	oldSilence := maxTriggerHeartbeatSilence
	triggerHeartbeatInterval = interval
	triggerHeartbeatTimeout = timeout
	maxTriggerHeartbeatSilence = silence
	t.Cleanup(func() {
		triggerHeartbeatInterval = oldInterval
		triggerHeartbeatTimeout = oldTimeout
		maxTriggerHeartbeatSilence = oldSilence
	})
}

func discardSlog() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestTriggerClaimHeartbeat_Reaped: controller returns 404 on the
// first heartbeat. Expect: killChild invoked, outcome=Reaped.
func TestTriggerClaimHeartbeat_Reaped(t *testing.T) {
	withFastTriggerHeartbeat(t, 5*time.Millisecond, 50*time.Millisecond, time.Second)

	ts, handler, _ := newTriggerHeartbeatServer(t)
	handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))

	cli := client.New(ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var killed atomic.Bool
	killChild := func() { killed.Store(true) }

	outcome := triggerClaimHeartbeat(ctx, cli, "trig-x", killChild, discardSlog())

	if outcome != triggerClaimReaped {
		t.Errorf("outcome=%v want triggerClaimReaped", outcome)
	}
	if !killed.Load() {
		t.Error("killChild not invoked on 404")
	}
}

// TestTriggerClaimHeartbeat_Silenced: controller returns 500 on
// every heartbeat. After silence window elapses, killChild invoked
// and outcome=Silenced.
func TestTriggerClaimHeartbeat_Silenced(t *testing.T) {
	withFastTriggerHeartbeat(t, 10*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond)

	ts, handler, _ := newTriggerHeartbeatServer(t)
	handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	cli := client.New(ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var killed atomic.Bool
	killChild := func() { killed.Store(true) }

	outcome := triggerClaimHeartbeat(ctx, cli, "trig-x", killChild, discardSlog())

	if outcome != triggerClaimSilenced {
		t.Errorf("outcome=%v want triggerClaimSilenced", outcome)
	}
	if !killed.Load() {
		t.Error("killChild not invoked after silence window")
	}
}

// TestTriggerClaimHeartbeat_TransientRecovery: controller fails a few
// times inside the silence window, then recovers. Heartbeat must
// stay alive and not invoke killChild.
func TestTriggerClaimHeartbeat_TransientRecovery(t *testing.T) {
	withFastTriggerHeartbeat(t, 5*time.Millisecond, 20*time.Millisecond, 500*time.Millisecond)

	ts, handler, _ := newTriggerHeartbeatServer(t)
	var reqs atomic.Int64
	handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqs.Add(1)
		if n <= 3 {
			http.Error(w, "blip", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	cli := client.New(ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var killed atomic.Bool
	killChild := func() { killed.Store(true) }

	outcome := triggerClaimHeartbeat(ctx, cli, "trig-x", killChild, discardSlog())

	if outcome != triggerClaimCtxDone {
		t.Errorf("outcome=%v want triggerClaimCtxDone (transient recovery must not kill)", outcome)
	}
	if killed.Load() {
		t.Error("killChild invoked despite recovery before silence window")
	}
}

// withFastPoolHeartbeat shrinks the node heartbeat timing knobs.
func withFastPoolHeartbeat(t *testing.T, timeout, silence time.Duration) {
	t.Helper()
	oldTimeout := poolHeartbeatTimeout
	oldSilence := poolHeartbeatMaxSilence
	poolHeartbeatTimeout = timeout
	poolHeartbeatMaxSilence = silence
	t.Cleanup(func() {
		poolHeartbeatTimeout = oldTimeout
		poolHeartbeatMaxSilence = oldSilence
	})
}

// TestRunPoolHeartbeat_ReapedCancelsNode: controller returns 409 on
// the first heartbeat (reaper flipped the claim). killNode must be
// invoked so the in-flight node execution stops.
func TestRunPoolHeartbeat_ReapedCancelsNode(t *testing.T) {
	withFastPoolHeartbeat(t, 50*time.Millisecond, time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "lost", http.StatusConflict)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cli := client.New(ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var killed atomic.Bool
	killNode := func() { killed.Store(true) }

	done := make(chan struct{})
	go func() {
		runPoolHeartbeat(ctx, cli, "run-1", "node-1", "holder-1",
			time.Minute, 5*time.Millisecond, killNode, "test", discardSlog())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runPoolHeartbeat did not return after ErrLockHeld")
	}
	if !killed.Load() {
		t.Error("killNode not invoked on 409")
	}
}

// TestRunPoolHeartbeat_SilenceCancelsNode: controller returns 500
// repeatedly. After silence window elapses, killNode invoked and
// heartbeat returns.
func TestRunPoolHeartbeat_SilenceCancelsNode(t *testing.T) {
	withFastPoolHeartbeat(t, 20*time.Millisecond, 100*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cli := client.New(ts.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var killed atomic.Bool
	killNode := func() { killed.Store(true) }

	done := make(chan struct{})
	go func() {
		runPoolHeartbeat(ctx, cli, "run-1", "node-1", "holder-1",
			time.Minute, 10*time.Millisecond, killNode, "test", discardSlog())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("runPoolHeartbeat did not return after silence window")
	}
	if !killed.Load() {
		t.Error("killNode not invoked after silence window")
	}
}
