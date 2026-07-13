package wingd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// TestQueueState_HostPressureExplainsWait sets a host under heavy external
// (non-sparkwing) load and, with a run already holding, admits a second run
// that needs more than the remaining headroom. The queue view must carry the
// headroom decomposition (reserve, external, available) and a per-waiter
// blocking reason naming the external load -- the queue is not silent about a
// host-pressure wait. A prior holder is present so the wait is real
// backpressure and not the liveness floor, which would admit a sole run.
func TestQueueState_HostPressureExplainsWait(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(10, 64<<30)
	sampler.set(wingd.HostStat{TotalCores: 10, TotalMemoryBytes: 64 << 30, FreeMemoryBytes: 64 << 30, LoadAverage: 3.2})
	startDaemon(t, wingd.Config{Home: home, Version: "v1", GraceWindow: -1, Sampler: sampler})

	holderClient := ensure(t, home, "v1")
	mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:     "holder",
		Resources: wingwire.HostResources{Cores: 1},
	})

	cl := ensure(t, home, "v1")
	_, result := acquireAsync(cl, wingwire.AdmissionRequest{
		RunID:     "heavy",
		Resources: wingwire.HostResources{Cores: 5},
	})
	select {
	case r := <-result:
		t.Fatalf("run was admitted (%v); it should queue on host pressure", r.err)
	case <-time.After(300 * time.Millisecond):
	}

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var cores *wingwire.ResourceState
	for i := range qs.Resources {
		if qs.Resources[i].Key == "cores" {
			cores = &qs.Resources[i]
		}
	}
	if cores == nil {
		t.Fatal("no cores resource row")
	}
	if cores.Reserved <= 0 || cores.External <= 0 {
		t.Fatalf("cores headroom = reserved %v external %v, want both positive", cores.Reserved, cores.External)
	}
	if cores.Available >= 5 {
		t.Fatalf("cores available = %v, want under the 5 the run needs (external load consumed it)", cores.Available)
	}
	if len(qs.Waiters) != 1 {
		t.Fatalf("waiters = %d, want 1", len(qs.Waiters))
	}
	reason := qs.Waiters[0].BlockingReason
	if !strings.Contains(reason, "needs") || !strings.Contains(reason, "available") || !strings.Contains(reason, "external load") {
		t.Fatalf("blocking reason = %q, want it to name needed, available, and external load", reason)
	}
}

// fakeProcSampler feeds controllable per-pid CPU fractions so stall
// flagging is exercised without a real busy or idle process.
type fakeProcSampler struct {
	mu    sync.Mutex
	usage map[int]wingd.ProcUsage
}

func (f *fakeProcSampler) CPUUsage(pid int) (wingd.ProcUsage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.usage[pid]
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

func TestQueueState_CarriesDaemonVersionAndUptime(t *testing.T) {
	home := shortHome(t)
	base := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	elapsed := time.Duration(0)
	startDaemon(t, wingd.Config{
		Home:             home,
		Version:          "v9.9.9",
		HeadroomFraction: -1,
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			elapsed += 250 * time.Millisecond
			return base.Add(elapsed)
		},
	})

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if qs.DaemonVersion != "v9.9.9" {
		t.Errorf("DaemonVersion = %q, want v9.9.9", qs.DaemonVersion)
	}
	if qs.DaemonUptimeMS <= 0 {
		t.Errorf("DaemonUptimeMS = %d, want > 0", qs.DaemonUptimeMS)
	}
}

// writeContainerFixture lays a minimal cgroup v2 tree under a fresh temp
// root, so a daemon pointed at it clamps capacity to the container limit.
func writeContainerFixture(t *testing.T, cpuMax, memMax string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("proc", "self", "cgroup"), "0::/\n")
	write(filepath.Join("sys", "fs", "cgroup", "cpu.max"), cpuMax)
	write(filepath.Join("sys", "fs", "cgroup", "memory.max"), memMax)
	return root
}

// TestQueueState_ClampsCapacityToContainerLimit points a daemon on a
// 24-core, 24 GiB host at a 6-core, 6 GiB cgroup and asserts the ledger,
// the container row, and an explicit budget compose: capacity is the
// container's, and a budget below it caps further.
func TestQueueState_ClampsCapacityToContainerLimit(t *testing.T) {
	home := shortHome(t)
	root := writeContainerFixture(t, "600000 100000", "6442450944")
	budget, err := wingd.ParseBudget("4,4gb")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(24, 24<<30),
		ContainerRoot:    root,
		Budget:           budget,
		HeadroomFraction: -1,
	})

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}

	if qs.Container == nil {
		t.Fatal("Container nil; want the cgroup limit reported")
	}
	if qs.Container.Cores != 6 || qs.Container.HostCores != 24 {
		t.Errorf("Container cores = %v (host %v), want 6 (host 24)", qs.Container.Cores, qs.Container.HostCores)
	}
	if qs.Container.MemoryBytes != 6<<30 || qs.Container.HostMemoryBytes != 24<<30 {
		t.Errorf("Container memory = %d (host %d), want %d (host %d)",
			qs.Container.MemoryBytes, qs.Container.HostMemoryBytes, int64(6)<<30, int64(24)<<30)
	}
	if qs.Budget == nil || qs.Budget.MachineCores != 6 {
		t.Errorf("Budget.MachineCores = %v, want 6 (container-clamped, not host 24)", qs.Budget)
	}
	if qs.Budget != nil && qs.Budget.Cores != 4 {
		t.Errorf("Budget.Cores = %v, want 4 (clamps below container)", qs.Budget.Cores)
	}
	for _, r := range qs.Resources {
		if r.Key == "cores" && r.Capacity != 4 {
			t.Errorf("cores capacity = %v, want 4 (budget below container below host)", r.Capacity)
		}
	}
}

// TestQueueState_NoContainerWhenUnlimited confirms a daemon whose cgroup
// imposes no limit reports the host totals and no container row.
func TestQueueState_NoContainerWhenUnlimited(t *testing.T) {
	home := shortHome(t)
	root := writeContainerFixture(t, "max 100000", "max")
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(8, 8<<30),
		ContainerRoot:    root,
		HeadroomFraction: -1,
	})

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	if qs.Container != nil {
		t.Fatalf("Container = %+v, want nil (unlimited cgroup)", qs.Container)
	}
	for _, r := range qs.Resources {
		if r.Key == "cores" && r.Capacity != 8 {
			t.Errorf("cores capacity = %v, want host 8", r.Capacity)
		}
	}
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
	proc := &fakeProcSampler{usage: map[int]wingd.ProcUsage{9001: {}}}
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
	proc := &fakeProcSampler{usage: map[int]wingd.ProcUsage{7001: {Fraction: 0.9}}}
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

func TestQueueState_IdleDescendantTreeStillStalls(t *testing.T) {
	home := shortHome(t)
	proc := &fakeProcSampler{usage: map[int]wingd.ProcUsage{8001: {HasDescendant: true}}}
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
		ProcSampler:      proc,
		StallInterval:    5 * time.Millisecond,
		StallWindow:      20 * time.Millisecond,
	})

	holder := ensure(t, home, "")
	mustAcquire(t, holder, semHostReq("idle-tree", "worker", 8001, "deploy"))

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, semHostReq("waiting", "builder", 8002, "deploy"))
	waitForQueue(t, positions)

	q := ensure(t, home, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		qs, err := q.QueueState(context.Background())
		if err != nil {
			t.Fatalf("queue state: %v", err)
		}
		if len(qs.Holders) == 1 && qs.Holders[0].Stalled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle descendant tree never flagged stalled: %+v", qs.Holders)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
