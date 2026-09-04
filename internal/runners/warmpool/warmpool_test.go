package warmpool

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type fallbackRunner struct {
	calls atomic.Int64
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (f *fallbackRunner) RunNode(context.Context, runner.Request) runner.Result {
	f.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

func newWarmPoolFixture(
	t *testing.T,
	needsLabels []string,
	wrap func(http.Handler, *store.Store) http.Handler,
) (*store.Store, *client.Client, func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(context.Background(), store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(context.Background(), store.Node{
		RunID: "run-1", NodeID: "build", Status: "pending", NeedsLabels: needsLabels,
	}); err != nil {
		t.Fatal(err)
	}
	handler := controller.New(st, quietTestLogger()).Handler()
	if wrap != nil {
		handler = wrap(handler, st)
	}
	srv := httptest.NewServer(handler)
	cleanup := func() {
		srv.Close()
		_ = st.Close()
	}
	return st, client.New(srv.URL, nil), cleanup
}

func TestRunnerCapsOfferWindowAtFiveSeconds(t *testing.T) {
	r := New(nil, nil, Config{ClaimWaitTimeout: time.Minute}, quietTestLogger())
	if r.cfg.ClaimWaitTimeout != 5*time.Second {
		t.Fatalf("claim wait = %s", r.cfg.ClaimWaitTimeout)
	}
}

func TestRunnerUsesRemoteClaimBeforeFallback(t *testing.T) {
	_, ctrl, cleanup := newWarmPoolFixture(t, nil, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: 200 * time.Millisecond,
	}, quietTestLogger())

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var claimed *store.Node
	for claimed == nil {
		var err error
		claimed, err = ctrl.ClaimNode(ctx, "agent:remote-workstation", nil, time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		if claimed == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := ctrl.FinishNode(ctx, "run-1", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result.Outcome != sparkwing.Success || result.Err != nil {
			t.Fatalf("result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("warm runner did not observe remote completion")
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
}

func TestRunnerFallsBackAfterClaimWindow(t *testing.T) {
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: 20 * time.Millisecond,
	}, quietTestLogger())

	result := r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	if result.Outcome != sparkwing.Success || result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if fallback.calls.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls.Load())
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt != nil {
		t.Fatalf("ready_at = %v, want revoked before fallback", node.ReadyAt)
	}
}

func TestRunnerFallsBackForLabelsItAdvertises(t *testing.T) {
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"location=coordinator", "gpu"}, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: 20 * time.Millisecond,
		FallbackLabels:   []string{"gpu", "location=coordinator", "local"},
	}, quietTestLogger())

	result := r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	if result.Outcome != sparkwing.Success || result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if fallback.calls.Load() != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.calls.Load())
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.Claimed || node.ReadyAt != nil {
		t.Fatalf("fallback node remains admitted: claimed=%v ready_at=%v", node.Claimed, node.ReadyAt)
	}
}

func TestRunnerClaimDuringFallbackHandoffPreventsDoubleExecution(t *testing.T) {
	claimed := make(chan struct{})
	revokeServed := make(chan struct{})
	var touches atomic.Int64
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, func(next http.Handler, st *store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/touch") {
				touches.Add(1)
			}
			if strings.HasSuffix(req.URL.Path, "/finalize-ready") {
				node, err := st.ClaimNextReadyNode(req.Context(), store.ClaimIdentity{
					Principal:   "remote-workstation",
					TokenPrefix: "swr_remote-workstation",
				}, "agent:remote-workstation", time.Minute, nil)
				if err != nil {
					t.Errorf("claim during handoff: %v", err)
				} else if node.NodeID != "build" {
					t.Errorf("claimed node = %q, want build", node.NodeID)
				}
				close(claimed)
				next.ServeHTTP(w, req)
				close(revokeServed)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:      5 * time.Millisecond,
		ClaimWaitTimeout:  10 * time.Millisecond,
		HeartbeatInterval: time.Millisecond,
	}, quietTestLogger())

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	}()

	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("runner never attempted the fallback handoff")
	}
	select {
	case <-revokeServed:
	case <-time.After(time.Second):
		t.Fatal("runner never completed the fallback handoff request")
	}
	time.Sleep(10 * time.Millisecond)
	touchCount := touches.Load()
	time.Sleep(10 * time.Millisecond)
	if got := touches.Load(); got != touchCount {
		t.Fatalf("pre-claim heartbeat continued after handoff: %d -> %d", touchCount, got)
	}
	if err := st.FinishNode(context.Background(), "run-1", "build", string(sparkwing.Failed), "remote failure", []byte(`{"remote":true}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case result := <-done:
		if result.Outcome != sparkwing.Failed || result.Err == nil || result.Err.Error() != "remote failure" {
			t.Fatalf("result = %+v, want remote completion", result)
		}
		if got, ok := result.Output.([]byte); !ok || string(got) != `{"remote":true}` {
			t.Fatalf("output = %#v, want remote output", result.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not observe remote completion")
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
}

func TestRunnerDoesNotFallbackLabeledNode(t *testing.T) {
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"os=windows", "gpu"}, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: 10 * time.Millisecond,
	}, quietTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	}()
	time.Sleep(40 * time.Millisecond)
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt == nil {
		t.Fatal("labeled node was removed from the compatible remote-agent queue")
	}

	cancel()
	select {
	case result := <-done:
		if result.Outcome != sparkwing.Cancelled {
			t.Fatalf("result = %+v, want cancelled", result)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
}

func TestRunnerCancellationRevokesUnclaimedNode(t *testing.T) {
	polled := make(chan struct{})
	var polledOnce sync.Once
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, func(next http.Handler, _ *store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodes/build") {
				polledOnce.Do(func() { close(polled) })
			}
		})
	})
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: time.Minute,
	}, quietTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	}()
	select {
	case <-polled:
	case <-time.After(10 * time.Second):
		t.Fatal("runner never reached the claim poll loop")
	}
	cancel()

	select {
	case result := <-done:
		if result.Outcome != sparkwing.Cancelled {
			t.Fatalf("result = %+v, want cancelled", result)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt != nil {
		t.Fatalf("ready_at = %v, want revoked on cancellation", node.ReadyAt)
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
}

func TestRunnerCancellationDuringMarkReadyReportsCancelled(t *testing.T) {
	cancels := make(chan context.CancelFunc, 1)
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, func(next http.Handler, _ *store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/mark-ready") {
				(<-cancels)()
				// safety: the caller is already gone, so the node must still reach ready for the revoke to matter
				next.ServeHTTP(w, req.WithContext(context.WithoutCancel(req.Context())))
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     5 * time.Millisecond,
		ClaimWaitTimeout: time.Minute,
	}, quietTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancels <- cancel
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"})
	}()

	select {
	case result := <-done:
		if result.Outcome != sparkwing.Cancelled {
			t.Fatalf("result = %+v, want cancelled", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.ReadyAt != nil {
		t.Fatalf("ready_at = %v, want revoked on cancellation", node.ReadyAt)
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
}

func TestRunnerObservesExpiredClaimFailure(t *testing.T) {
	st, ctrl, cleanup := newWarmPoolFixture(t, nil, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:      time.Millisecond,
		ClaimWaitTimeout:  time.Second,
		HeartbeatInterval: time.Millisecond,
	}, quietTestLogger())

	done := make(chan runner.Result, 1)
	go func() {
		done <- r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	}()
	claimDeadline := time.After(time.Second)
	for {
		node, err := st.ClaimNextReadyNode(context.Background(), store.ClaimIdentity{
			Principal:   "offline-server",
			TokenPrefix: "swr_offline-server",
		}, "agent:offline-server", 10*time.Millisecond, nil)
		if err == nil {
			if node.NodeID != "build" {
				t.Fatalf("claimed node = %q, want build", node.NodeID)
			}
			break
		}
		if err != store.ErrNotFound {
			t.Fatal(err)
		}
		select {
		case <-claimDeadline:
			t.Fatal("node did not become ready for the remote executor")
		case <-time.After(time.Millisecond):
		}
	}
	observedDeadline := time.After(time.Second)
	for {
		node, err := st.GetNode(context.Background(), "run-1", "build")
		if err != nil {
			t.Fatal(err)
		}
		if node.StatusDetail == "claimed by remote executor" {
			break
		}
		select {
		case <-observedDeadline:
			t.Fatal("warm runner did not observe the active remote claim")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond)
	pairs, err := store.Maintenance.FailExpiredNodeClaims(st, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expired claims = %v, want run-1/build", pairs)
	}

	select {
	case result := <-done:
		if result.Outcome != sparkwing.Failed || result.Err == nil || result.Err.Error() != "runner heartbeat expired" {
			t.Fatalf("result = %+v, want bounded agent-lost failure", result)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not observe expired claim failure")
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
}
