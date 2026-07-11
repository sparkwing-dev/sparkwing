package wingd_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// fakeProcSampler feeds controllable per-pid CPU fractions so stall
// flagging is exercised without a real busy or idle process.
type fakeProcSampler struct {
	mu  sync.Mutex
	cpu map[int]float64
}

func (f *fakeProcSampler) CPUFraction(pid int) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.cpu[pid]
	return v, ok
}

func semHostReq(runID, pipeline string, pid int, key string) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:     runID,
		Pipeline:  pipeline,
		PID:       pid,
		Resources: wingwire.HostResources{Cores: 1},
		Semaphores: []wingwire.SemaphoreClaim{
			{Name: key, Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue},
		},
	}
}

func waiterByRun(qs wingwire.QueueState, runID string) (wingwire.Waiter, bool) {
	for _, w := range qs.Waiters {
		if w.RunID == runID {
			return w, true
		}
	}
	return wingwire.Waiter{}, false
}

func TestQueueState_ReportsHoldersWaitersPositionsAndPipelines(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("holder", "deployer", 4242, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, semHostReq("waiter", "builder", 4343, "deploy"))
	waitForQueue(t, positions)

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}

	if got := resourceHeld(qs, "deploy"); got != 1 {
		t.Fatalf("deploy held %v, want 1 (matches the one holder)", got)
	}
	if got := resourceHeld(qs, "cores"); got != 1 {
		t.Fatalf("cores held %v, want 1", got)
	}

	if len(qs.Holders) != 1 {
		t.Fatalf("holders = %+v, want exactly the deploy holder", qs.Holders)
	}
	h := qs.Holders[0]
	if h.RunID != "holder" || h.Pipeline != "deployer" {
		t.Fatalf("holder identity = %q/%q, want holder/deployer", h.RunID, h.Pipeline)
	}
	if len(h.Semaphores) != 1 || h.Semaphores[0] != "deploy" {
		t.Fatalf("holder semaphores = %v, want [deploy]", h.Semaphores)
	}
	if h.Stalled {
		t.Fatalf("a fresh holder must not be flagged stalled")
	}

	w, ok := waiterByRun(qs, "waiter")
	if !ok {
		t.Fatalf("waiter not present in queue: %+v", qs.Waiters)
	}
	if w.Position != 1 {
		t.Fatalf("waiter position = %d, want 1 (only waiter)", w.Position)
	}
	if w.Pipeline != "builder" {
		t.Fatalf("waiter pipeline = %q, want builder", w.Pipeline)
	}
	if len(w.WaitingOn) == 0 || w.WaitingOn[0] != "deploy" {
		t.Fatalf("waiter waiting_on = %v, want [deploy] (the full semaphore)", w.WaitingOn)
	}
}

func TestQueueState_FlagsStalledHolderWithRecoveryCommand(t *testing.T) {
	home := shortHome(t)
	proc := &fakeProcSampler{cpu: map[int]float64{9001: 0}}
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
		ProcSampler:      proc,
		StallInterval:    5 * time.Millisecond,
		StallWindow:      20 * time.Millisecond,
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("wedged", "stuck", 9001, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, semHostReq("waiting", "builder", 9002, "deploy"))
	waitForQueue(t, positions)

	q := ensure(t, home, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		qs, err := q.QueueState(context.Background())
		if err != nil {
			t.Fatalf("queue state: %v", err)
		}
		if len(qs.Holders) == 1 && qs.Holders[0].Stalled {
			if qs.Holders[0].Recovery != "sparkwing runs cancel --run wedged" {
				t.Fatalf("recovery command = %q, want the non-destructive cancel", qs.Holders[0].Recovery)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle holder never flagged stalled: %+v", qs.Holders)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQueueState_BusyHolderIsNotStalled(t *testing.T) {
	home := shortHome(t)
	proc := &fakeProcSampler{cpu: map[int]float64{7001: 0.9}}
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
		ProcSampler:      proc,
		StallInterval:    5 * time.Millisecond,
		StallWindow:      20 * time.Millisecond,
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("busy", "worker", 7001, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, semHostReq("waiting", "builder", 7002, "deploy"))
	waitForQueue(t, positions)

	time.Sleep(150 * time.Millisecond)

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if len(qs.Holders) != 1 {
		t.Fatalf("holders = %+v, want one", qs.Holders)
	}
	if qs.Holders[0].Stalled {
		t.Fatalf("a busy holder must never be flagged stalled")
	}
}
