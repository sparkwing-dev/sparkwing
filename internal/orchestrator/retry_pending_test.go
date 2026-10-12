package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	gatedRetryBodies atomic.Int32
	gatedRetryGate   chan struct{}
)

// safety: only the first run fails and every later one waits on the gate, so the
// node can be read between attempts before the retry finishes it.
func gatedRetryBody(context.Context) error {
	if gatedRetryBodies.Add(1) == 1 {
		return errors.New("first attempt fails")
	}
	<-gatedRetryGate
	return nil
}

type gatedRetryPipe struct {
	sparkwing.Base
	inline, auto bool
}

func (p gatedRetryPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	opts := []sparkwing.RetryOption{sparkwing.RetryBackoff(50 * time.Millisecond)}
	if p.auto {
		opts = append(opts, sparkwing.RetryAuto())
	}
	job := sparkwing.Job(plan, "gated", gatedRetryBody).Retry(1, opts...)
	if p.inline {
		job.Inline()
	}
	return nil
}

func init() {
	register("retry-pending-auto", func() sparkwing.Pipeline[sparkwing.NoInputs] { return gatedRetryPipe{auto: true} })
	register("retry-pending-auto-inline", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return gatedRetryPipe{auto: true, inline: true}
	})
	register("retry-pending-in-process", func() sparkwing.Pipeline[sparkwing.NoInputs] { return gatedRetryPipe{} })
}

func resetGatedRetry() {
	gatedRetryBodies.Store(0)
	gatedRetryGate = make(chan struct{})
}

func awaitNodeEvent(t *testing.T, ctx context.Context, st *store.Store, runID, nodeID, kind string) {
	t.Helper()
	for {
		events, err := st.ListEventsAfter(ctx, runID, 0, 1000)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.NodeID == nodeID && e.Kind == kind {
				return
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("no %s event for %s: %v", kind, nodeID, ctx.Err())
		}
		runtime.Gosched()
	}
}

func wantNodeNotDone(t *testing.T, ctx context.Context, st *store.Store, runID string) {
	t.Helper()
	node, err := st.GetNode(ctx, runID, "gated")
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if node.Status == "done" {
		t.Fatalf("node reads done/%s while its retry is still to come", node.Outcome)
	}
}

// A node with a retry still to come is never done, on every path that
// retries it: the dispatcher's RetryAuto resets it before its backoff, in
// process, inline, under a trigger's claim, or claimed by a remote runner,
// and an in-process retry keeps it running. The store's own retry is
// covered by TestReportAttempt_ARetryingFailureNeverReleasesDependents.
func TestRetryPending_NodeIsNeverDoneBeforeItsRetry(t *testing.T) {
	type setup struct {
		ctx      context.Context
		backends orchestrator.Backends
		runner   runner.Runner
	}
	cases := []struct {
		name, pipeline, event string
		build                 func(t *testing.T, paths orchestrator.Paths, st *store.Store, runID string) setup
	}{
		{name: "auto in process", pipeline: "retry-pending-auto", event: "node_auto_retry"},
		{name: "auto inline", pipeline: "retry-pending-auto-inline", event: "node_auto_retry"},
		{name: "in-process retry", pipeline: "retry-pending-in-process"},
		{
			name: "auto trigger-owned", pipeline: "retry-pending-auto", event: "node_auto_retry",
			build: func(t *testing.T, paths orchestrator.Paths, st *store.Store, runID string) setup {
				claimCtx, cancel := context.WithTimeout(claimedRetryContext(t, st, runID, "retry-pending-auto"), 10*time.Second)
				t.Cleanup(cancel)
				return setup{ctx: claimCtx, backends: orchestrator.LocalBackends(paths, st, nil)}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetGatedRetry()
			paths := newPathsWithStore(t)
			st, err := store.Open(paths.StateDB())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			runID := "retry-pending-" + fmt.Sprint(time.Now().UnixNano())
			s := setup{ctx: ctx, backends: orchestrator.LocalBackends(paths, st, nil)}
			if tc.build != nil {
				s = tc.build(t, paths, st, runID)
			}
			done := make(chan *orchestrator.Result, 1)
			go func() {
				res, runErr := orchestrator.Run(s.ctx, s.backends, orchestrator.Options{
					Pipeline: tc.pipeline, RunID: runID, Runner: s.runner,
				})
				if runErr != nil {
					t.Error(runErr)
				}
				done <- res
			}()
			if tc.event != "" {
				awaitNodeEvent(t, ctx, st, runID, "gated", tc.event)
				wantNodeNotDone(t, ctx, st, runID)
			} else {
				for gatedRetryBodies.Load() < 2 {
					wantNodeNotDone(t, ctx, st, runID)
					runtime.Gosched()
				}
			}
			close(gatedRetryGate)
			if res := <-done; res == nil || res.Status != "success" {
				t.Fatalf("result = %+v, want success after one retry", res)
			}
		})
	}
}

// A remote runner's reported failure leaves the node pending through the
// dispatcher's backoff too, before the runner claims it again.
func TestRetryPending_ClaimedNodeIsNotDoneBeforeItsRetry(t *testing.T) {
	paths := newPathsWithStore(t)
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := orchestrator.NewControllerServer(t, st, nil)
	defer server.Close()
	warm := warmpool.New(client.New(server.URL, nil), nil, warmpool.Config{
		PollInterval: time.Millisecond, ClaimWaitTimeout: time.Second,
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const runID = "retry-pending-claimed"
	gate := make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		for attempt := 1; attempt <= 2; attempt++ {
			var claimed *store.Node
			for {
				n, claimErr := st.ClaimNextReadyNode(ctx, store.ClaimIdentity{}, fmt.Sprintf("agent:warm:%d", attempt), time.Minute, nil)
				if claimErr == nil {
					claimed = n
					break
				}
				if !errors.Is(claimErr, store.ErrNotFound) {
					workerDone <- claimErr
					return
				}
				if ctx.Err() != nil {
					workerDone <- ctx.Err()
					return
				}
				runtime.Gosched()
			}
			if attempt == 1 {
				if err := st.FinishNode(ctx, claimed.RunID, claimed.NodeID, "failed", "first attempt fails", nil); err != nil {
					workerDone <- err
					return
				}
				continue
			}
			<-gate
			workerDone <- st.FinishNode(ctx, claimed.RunID, claimed.NodeID, "success", "", nil)
			return
		}
	}()
	done := make(chan *orchestrator.Result, 1)
	go func() {
		res, runErr := orchestrator.Run(ctx, orchestrator.LocalBackends(paths, st, nil), orchestrator.Options{
			Pipeline: "retry-pending-auto", RunID: runID, Runner: warm,
		})
		if runErr != nil {
			t.Error(runErr)
		}
		done <- res
	}()
	awaitNodeEvent(t, ctx, st, runID, "gated", "node_auto_retry")
	wantNodeNotDone(t, ctx, st, runID)
	close(gate)
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	if res := <-done; res == nil || res.Status != "success" {
		t.Fatalf("result = %+v, want success after one retry", res)
	}
}
