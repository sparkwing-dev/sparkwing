package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// wedgeRelease lets the test unblock the deliberately-stuck node body
// after the watchdog has fired, so the leaked goroutine drains during
// teardown instead of haunting subsequent tests in the same process.
var wedgeRelease = make(chan struct{})

type wedgedNodePipe struct{ sparkwing.Base }

func (wedgedNodePipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "wedged", func(ctx context.Context) error {
		<-wedgeRelease
		return nil
	})
	return nil
}

func init() {
	register("wedged-node", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &wedgedNodePipe{} })
}

// TestDispatchWatchdog_FiresOnStuckNode: a node whose body ignores ctx
// and never returns leaves a wg slot incremented forever. With a
// short DispatchWaitTimeout the dispatcher must (a) return with an
// error mentioning dispatch_wait_timeout, (b) emit the watchdog event
// into both the envelope and the state events table, (c) name the
// stuck node, and (d) do all of this within a small multiple of the
// timeout (no hidden additional wait).
func TestDispatchWatchdog_FiresOnStuckNode(t *testing.T) {
	t.Cleanup(func() {
		select {
		case <-wedgeRelease:
		default:
			close(wedgeRelease)
		}
	})
	p := newPaths(t)

	start := time.Now()
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:            "wedged-node",
		DispatchWaitTimeout: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunLocal returned an unexpected error: %v", err)
	}
	if res == nil || res.Status != "failed" || res.Error == nil {
		t.Fatalf("res = %+v, want status=failed with non-nil Error", res)
	}
	runErr := res.Error.Error()
	if !strings.Contains(runErr, "dispatch_wait_timeout") {
		t.Errorf("res.Error %q must mention dispatch_wait_timeout", runErr)
	}
	if !strings.Contains(runErr, "wedged") {
		t.Errorf("res.Error %q must name the stuck node", runErr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("dispatcher returned after %s; watchdog should have fired near 300ms", elapsed)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	events, _ := st.ListEventsAfter(context.Background(), res.RunID, 0, 500)
	found := false
	for _, e := range events {
		if e.Kind == "dispatch_wait_timeout" {
			found = true
			payload := string(e.Payload)
			for _, want := range []string{"wedged", "timeout", "stuck_nodes"} {
				if !strings.Contains(payload, want) {
					t.Errorf("dispatch_wait_timeout payload missing %q: %s", want, payload)
				}
			}
			break
		}
	}
	if !found {
		t.Errorf("no dispatch_wait_timeout event in run stream")
	}

	node, gerr := st.GetNode(context.Background(), res.RunID, "wedged")
	if gerr != nil {
		t.Fatalf("GetNode wedged: %v", gerr)
	}
	if node.Status != "done" || node.Outcome != "cancelled" {
		t.Errorf("wedged node status/outcome = %q/%q, want done/cancelled after dispatch timeout", node.Status, node.Outcome)
	}
}

// TestDispatchWatchdog_NegativeDisables: a negative timeout opts out
// of the watchdog (the historical wait-forever behavior). We assert
// the opt-out is reachable without actually hanging the test: a node
// that respects ctx cancellation finishes normally, so a -1 timeout
// must produce a clean success.
func TestDispatchWatchdog_NegativeDisables(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	p := newPaths(t)

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:            "spawn-single",
		DispatchWaitTimeout: -1,
	})
	wg.Done()
	if err != nil {
		t.Fatalf("normal run with disabled watchdog: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
}
