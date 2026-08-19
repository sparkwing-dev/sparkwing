package orchestrator_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestMaxParallel_CapsConcurrentNodeExecution verifies that
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
	entered := make(chan struct{}, fanOut)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseJobs := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseJobs)

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
			entered:        entered,
			release:        release,
		}
	})

	p := newPaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var res *orchestrator.Result
	var runErr error
	done := make(chan struct{})
	go func() {
		res, runErr = orchestrator.RunLocal(ctx, p, orchestrator.Options{
			Pipeline:    "orch-maxparallel",
			MaxParallel: cap,
		})
		close(done)
	}()
	t.Cleanup(func() {
		releaseJobs()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("RunLocal did not stop during cleanup")
		}
	})
	for range cap {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("configured capacity was not admitted")
		}
	}
	select {
	case <-entered:
		t.Fatal("a fifth job entered while four jobs held the configured capacity")
	case <-time.After(200 * time.Millisecond):
	}
	releaseJobs()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("RunLocal did not finish after jobs were released")
	}
	err := runErr
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
		t.Fatal("active counter never observed at the post-acquire baseline; test is faulty")
	}
}

type maxParallelPipe struct {
	sparkwing.Base
	fanOut         int
	active         *atomic.Int32
	peak           *atomic.Int32
	observedAtZero *atomic.Bool
	entered        chan<- struct{}
	release        chan struct{}
}

func (p *maxParallelPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	for i := range p.fanOut {
		id := jobName(i)
		sparkwing.Job(plan, id, func(ctx context.Context) error {
			n := p.active.Add(1)
			defer p.active.Add(-1)
			if n == 1 {
				p.observedAtZero.Store(true)
			}
			for {
				old := p.peak.Load()
				if n <= old || p.peak.CompareAndSwap(old, n) {
					break
				}
			}
			p.entered <- struct{}{}
			select {
			case <-p.release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
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
