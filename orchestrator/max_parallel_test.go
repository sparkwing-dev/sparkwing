package orchestrator_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
)

// TestMaxParallel_CapsConcurrentNodeExecution verifies LOCAL-006:
// dispatchState's semaphore caps how many activeRunner.RunNode calls
// run at once when Options.MaxParallel > 0. Builds a fan-out plan
// with 30 sibling jobs, each of which records "active goroutine
// count" via shared atomic counters; assertion is that the observed
// peak never exceeds MaxParallel.
func TestMaxParallel_CapsConcurrentNodeExecution(t *testing.T) {
	const fanOut = 30
	const cap = 4

	var active, peak atomic.Int32
	var observedAtZero atomic.Bool

	registerOnce.Range(func(k, _ any) bool {
		if k.(string) == "orch-maxparallel" {
			registerOnce.Delete(k)
		}
		return true
	})

	register("orch-maxparallel", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return &maxParallelPipe{
			fanOut:         fanOut,
			active:         &active,
			peak:           &peak,
			observedAtZero: &observedAtZero,
		}
	})

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:    "orch-maxparallel",
		MaxParallel: cap,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}
	if got := peak.Load(); got > int32(cap) {
		t.Fatalf("peak concurrent = %d, want <= %d", got, cap)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrent = %d, want >= 2 (no parallelism observed at all -- "+
			"semaphore may be over-restricting)", peak.Load())
	}
	if !observedAtZero.Load() {
		// Sanity: at the start of the very first job, active should be 1.
		// If we never saw active==1 before incrementing, the counter wiring is wrong.
		t.Fatal("active counter never observed at the post-acquire baseline; test is faulty")
	}
}

type maxParallelPipe struct {
	sparkwing.Base
	fanOut         int
	active         *atomic.Int32
	peak           *atomic.Int32
	observedAtZero *atomic.Bool
}

func (p *maxParallelPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	for i := range p.fanOut {
		id := jobName(i)
		sparkwing.Job(plan, id, func(ctx context.Context) error {
			n := p.active.Add(1)
			if n == 1 {
				p.observedAtZero.Store(true)
			}
			for {
				old := p.peak.Load()
				if n <= old || p.peak.CompareAndSwap(old, n) {
					break
				}
			}
			// Hold the slot long enough that the dispatcher saturates
			// the cap. 50ms * cap is well under test budgets but long
			// enough that 30 jobs at cap=4 take ~400ms instead of
			// ~50ms.
			time.Sleep(50 * time.Millisecond)
			p.active.Add(-1)
			return nil
		})
	}
	return nil
}

func jobName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < 26 {
		return "job-" + string(letters[i])
	}
	return "job-" + string(letters[i/26-1]) + string(letters[i%26])
}

// silence unused import on go versions without strconv usage above
var _ = sync.Mutex{}
