package warmpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
	calls  atomic.Int64
	labels []string
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (f *fallbackRunner) RunNode(context.Context, runner.Request) runner.Result {
	f.calls.Add(1)
	return runner.Result{Outcome: sparkwing.Success}
}

func (f *fallbackRunner) AdvertisedLabels() []string {
	return append([]string(nil), f.labels...)
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

func TestRunnerBatchesRemoteNodeWait(t *testing.T) {
	testRunnerBatchesRemoteNodeWait(t, time.Second, 5*time.Millisecond)
}

func TestRunnerBatchesRemoteNodeWaitForTwoMinutes(t *testing.T) {
	if testing.Short() {
		t.Skip("two-minute request budget measurement")
	}
	testRunnerBatchesRemoteNodeWait(t, 2*time.Minute, 500*time.Millisecond)
}

func testRunnerBatchesRemoteNodeWait(t *testing.T, duration, pollInterval time.Duration) {
	var remoteReads atomic.Int64
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"github-actions"}, func(next http.Handler, _ *store.Store) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/api/v1/runs/run-1/nodes") {
				remoteReads.Add(1)
			}
			next.ServeHTTP(w, req)
		})
	})
	defer cleanup()
	for i := 1; i < 8; i++ {
		if err := st.CreateNode(context.Background(), store.Node{RunID: "run-1", NodeID: fmt.Sprintf("node-%d", i), Status: "pending", NeedsLabels: []string{"github-actions"}}); err != nil {
			t.Fatal(err)
		}
	}
	r := New(ctrl, nil, Config{PollInterval: pollInterval, UnmatchableGrace: duration + time.Minute}, quietTestLogger())
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range []string{"build", "node-1", "node-2", "node-3", "node-4", "node-5", "node-6", "node-7"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: id})
		}()
	}
	wg.Wait()
	if got := remoteReads.Load(); got >= 60 {
		t.Fatalf("8 remote nodes made %d status requests in %s, want <60", got, duration)
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
	if testing.Short() {
		t.Skip("slow: 5.1s of real work; the fast class runs under -short")
	}
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
	if testing.Short() {
		t.Skip("slow: 5.1s of real work; the fast class runs under -short")
	}
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"location=coordinator", "gpu"}, nil)
	defer cleanup()
	fallback := &fallbackRunner{labels: []string{"gpu", "location=coordinator", "local"}}
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
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodes") {
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
		if !errors.Is(err, store.ErrNotFound) {
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

func TestNewDefaultsTheHeartbeatToTheStoreCadence(t *testing.T) {
	r := New(nil, nil, Config{}, nil)
	if r.cfg.HeartbeatInterval != store.DispatchedHeartbeatInterval {
		t.Fatalf("default heartbeat = %s, want the store's %s cadence the charge cap is judged against",
			r.cfg.HeartbeatInterval, store.DispatchedHeartbeatInterval)
	}
}

// The grace runs from the first sighting, and a wait exactly as long as it is
// still inside it.
func TestUnmatchableExpiredAtBothEdges(t *testing.T) {
	t.Parallel()
	since := time.Unix(0, 0)
	grace := 10 * time.Millisecond
	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "the first sighting", now: since, want: false},
		{name: "one nanosecond inside the grace", now: since.Add(grace - time.Nanosecond), want: false},
		{name: "exactly the grace", now: since.Add(grace), want: false},
		{name: "one nanosecond past the grace", now: since.Add(grace + time.Nanosecond), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unmatchableExpired(since, tc.now, grace); got != tc.want {
				t.Fatalf("unmatchableExpired = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestNewDefaultsTheUnmatchableGrace(t *testing.T) {
	t.Parallel()
	if got := New(nil, nil, Config{}, quietTestLogger()).cfg.UnmatchableGrace; got != DefaultUnmatchableGrace {
		t.Fatalf("grace = %s, want %s", got, DefaultUnmatchableGrace)
	}
	if got := New(nil, nil, Config{UnmatchableGrace: time.Second}, quietTestLogger()).cfg.UnmatchableGrace; got != time.Second {
		t.Fatalf("grace = %s, want the configured second", got)
	}
}

// A node no runner advertises and no fallback may take fails once its grace
// runs out, and says which labels and which class it failed on.
func TestRunnerDoesNotRouteRequiredLabelsToUnlabelledFallback(t *testing.T) {
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"os=windows", "gpu"}, nil)
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     time.Millisecond,
		ClaimWaitTimeout: 5 * time.Millisecond,
		UnmatchableGrace: time.Millisecond,
	}, quietTestLogger())

	result := r.RunNode(context.Background(), runner.Request{RunID: "run-1", NodeID: "build"})
	if result.Outcome != sparkwing.Failed {
		t.Fatalf("result = %+v, want failed", result)
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.Outcome != string(sparkwing.Failed) || node.FailureReason != store.FailureQueueTimeout {
		t.Fatalf("node = %s/%s, want a queue timeout", node.Outcome, node.FailureReason)
	}

	events, err := st.ListEventsAfter(context.Background(), "run-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found *UnmatchableEvent
	for _, e := range events {
		if e.Kind != "node_unmatchable" {
			continue
		}
		var payload UnmatchableEvent
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("decode the event: %v: %s", err, e.Payload)
		}
		found = &payload
	}
	if found == nil {
		t.Fatal("no node_unmatchable event was recorded")
	}
	if !reflect.DeepEqual(found.NeedsLabels, []string{"os=windows", "gpu"}) {
		t.Errorf("needs_labels = %v", found.NeedsLabels)
	}
	if len(found.FallbackLabels) != 0 {
		t.Errorf("fallback_labels = %v, want none advertised", found.FallbackLabels)
	}
	if found.GraceSeconds != time.Millisecond.Seconds() {
		t.Errorf("grace_seconds = %v, want %v", found.GraceSeconds, time.Millisecond.Seconds())
	}
	if !strings.Contains(found.Detail, "os=windows") {
		t.Errorf("detail = %q, want it to name the labels", found.Detail)
	}
}

// A grace that has not run out leaves the node queued for a runner that may
// still appear.
func TestRunnerKeepsAnUnmatchableNodeInsideItsGrace(t *testing.T) {
	polled := make(chan struct{})
	var polledOnce sync.Once
	st, ctrl, cleanup := newWarmPoolFixture(t, []string{"os=windows"}, func(next http.Handler, _ *store.Store) http.Handler {
		var polls atomic.Int64
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/nodes") &&
				polls.Add(1) >= 5 {
				polledOnce.Do(func() { close(polled) })
			}
		})
	})
	defer cleanup()
	fallback := &fallbackRunner{}
	r := New(ctrl, fallback, Config{
		PollInterval:     time.Millisecond,
		ClaimWaitTimeout: 2 * time.Millisecond,
		UnmatchableGrace: time.Hour,
	}, quietTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runner.Result, 1)
	go func() { done <- r.RunNode(ctx, runner.Request{RunID: "run-1", NodeID: "build"}) }()
	// safety: the package timeout is what bounds a runner that never polls, so
	// the test waits on the signal itself rather than on the wall clock.
	<-polled
	node, err := st.GetNode(context.Background(), "run-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.Outcome != "" {
		t.Fatalf("node outcome = %q inside its grace, want it still queued", node.Outcome)
	}
	if fallback.calls.Load() != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallback.calls.Load())
	}

	cancel()
	if result := <-done; result.Outcome != sparkwing.Cancelled {
		t.Fatalf("result = %+v, want cancelled", result)
	}
}
