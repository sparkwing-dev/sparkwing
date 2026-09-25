package cluster

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

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

type triggerHeartbeatTransport func(http.ResponseWriter, *http.Request)

func (f triggerHeartbeatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	f(w, req)
	return w.Result(), nil
}

func TestTriggerClaimHeartbeat_Reaped(t *testing.T) {
	withFastTriggerHeartbeat(t, 5*time.Millisecond, 50*time.Millisecond, time.Second)

	ts, handler, _ := newTriggerHeartbeatServer(t)
	handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"gone"}`))
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

func TestTriggerClaimHeartbeat_StopsChildOnClaimSignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respond triggerHeartbeatTransport
	}{
		{"conflict", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "claim stopped", http.StatusConflict)
		}},
		{"cancel request", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"cancel_requested":true}`))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				killed := false
				httpClient := &http.Client{Transport: tc.respond}
				got := triggerClaimHeartbeat(t.Context(), client.New("http://controller.test", httpClient),
					"trig-x", func() { killed = true }, discardSlog())
				if got != triggerClaimReaped || !killed {
					t.Fatalf("%s left child running: outcome=%v killed=%v", tc.name, got, killed)
				}
			})
		})
	}
}

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

func TestTriggerClaimHeartbeat_TransientRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
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
			time.Minute, 5*time.Millisecond, killNode, "test", nil, discardSlog())
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
			time.Minute, 10*time.Millisecond, killNode, "test", nil, discardSlog())
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
