package nodemetrics

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

type captureSink struct {
	mu          sync.Mutex
	samples     []Sample
	memoryReady chan struct{}
	memoryOnce  sync.Once
	sampleReady chan struct{}
}

func (s *captureSink) Push(_ context.Context, sample Sample) error {
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	if sample.MemoryBytes > 0 && s.memoryReady != nil {
		s.memoryOnce.Do(func() { close(s.memoryReady) })
	}
	s.mu.Unlock()
	if s.sampleReady != nil {
		select {
		case s.sampleReady <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *captureSink) peakCPU() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peak int64
	for _, sm := range s.samples {
		if sm.CPUMillicores > peak {
			peak = sm.CPUMillicores
		}
	}
	return peak
}

func (s *captureSink) hasSampleAfter(boundary time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sample := range s.samples {
		if sample.TS.After(boundary) {
			return true
		}
	}
	return false
}

func (s *captureSink) waitForSampleAfter(ctx context.Context, boundary time.Time) error {
	for !s.hasSampleAfter(boundary) {
		select {
		case <-s.sampleReady:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

const cpuAccountingBurnerEnv = "SPARKWING_NODEMETRICS_CPU_BURNER"

func TestCPUAccountingBurnerProcess(t *testing.T) {
	if os.Getenv(cpuAccountingBurnerEnv) != "1" {
		return
	}
	var value uint64 = 1
	for i := 0; i < 25_000_000; i++ {
		value = value*1664525 + 1013904223
	}
	runtime.KeepAlive(value)
}

// TestAttach_ReportsNonzeroCPUUnderLoad verifies that on the host platform a
// CPU-burning process must produce a nonzero sampled peak, so learned
// capacity can activate rather than costing every run by the default.
func TestAttach_ReportsNonzeroCPUUnderLoad(t *testing.T) {
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detach := Attach(ctx, sink)

	burnUntil := time.Now().Add(600 * time.Millisecond)
	x := 0
	for time.Now().Before(burnUntil) {
		x++
		_ = x * x
	}
	detach()
	waitForSamplerStop(t)

	if peak := sink.peakCPU(); peak <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 after burning a core", peak)
	}
}

// TestAttach_CountsRawExecChildrenCPU verifies that CPU burned by a child
// spawned with os/exec outside the SDK command wrapper surfaces in the
// sampled peak through RUSAGE_CHILDREN, so a raw-exec pipeline cannot measure
// zero and be over-admitted at the floor. The parent stays near idle while
// each child burns, so the peak reflects the children rather than self.
func TestAttach_CountsRawExecChildrenCPU(t *testing.T) {
	if _, ok := readCPUTime(); !ok {
		t.Skip("no CPU accounting on this platform")
	}
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	sink := &captureSink{sampleReady: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	detach := Attach(ctx, sink)
	t.Cleanup(func() {
		detach()
		cancel()
		waitForSamplerStop(t)
	})

	for sink.peakCPU() <= 300 {
		burnAndReap(t)
		reapedAt := time.Now()
		if err := sink.waitForSampleAfter(ctx, reapedAt); err != nil {
			t.Fatalf("wait for CPU sample after reaping child: %v", err)
		}
	}
	detach()
	waitForSamplerStop(t)

	if peak := sink.peakCPU(); peak <= 300 {
		t.Fatalf("peak CPU millicores = %d, want > 300 from raw-exec child burn", peak)
	}
}

// burnAndReap runs fixed CPU work in a child and waits for its usage to land
// in RUSAGE_CHILDREN.
func burnAndReap(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCPUAccountingBurnerProcess$")
	cmd.Env = append(os.Environ(), cpuAccountingBurnerEnv+"=1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run CPU burner: %v", err)
	}
}

// TestReadCPUTime_SubtractsReportedChildCPU pins the reconciliation: a reaped
// child raises the cumulative reading through RUSAGE_CHILDREN, and reporting
// its CPU (as the SDK per-command path does) brings the reading back down, so
// the same usage is not counted twice.
//
// How much CPU a child wins inside one wall-clock window is the scheduler's to
// decide, so the burn repeats until the reading has risen far enough to be
// unambiguous instead of assuming one window earned it. A busy box needs more
// windows, not a looser floor.
func TestReadCPUTime_SubtractsReportedChildCPU(t *testing.T) {
	base, ok := readCPUTime()
	if !ok {
		t.Skip("no CPU accounting on this platform")
	}

	const (
		wantChildCPU = 100 * time.Millisecond
		burnDeadline = 5 * time.Second
	)
	var withChild, childCPU time.Duration
	giveUp := time.Now().Add(burnDeadline)
	for childCPU < wantChildCPU && time.Now().Before(giveUp) {
		burnAndReap(t)
		withChild, _ = readCPUTime()
		childCPU = withChild - base
	}
	if childCPU < wantChildCPU {
		t.Fatalf("reaped children raised reading by %s over %s, want at least %s via RUSAGE_CHILDREN", childCPU, burnDeadline, wantChildCPU)
	}

	AddReportedChildCPU(childCPU)
	reconciled, _ := readCPUTime()
	if reconciled >= withChild-childCPU/2 {
		t.Fatalf("reading after reporting = %s, want the child's %s subtracted back out", reconciled, childCPU)
	}
}

// TestAttach_ReportsMemory asserts the sampler reports a nonzero memory
// reading from the platform RSS source or its runtime fallback.
func TestAttach_ReportsMemory(t *testing.T) {
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	memoryReady := make(chan struct{})
	sink := &captureSink{memoryReady: memoryReady}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	detach := Attach(ctx, sink)
	t.Cleanup(func() {
		detach()
		cancel()
		waitForSamplerStop(t)
	})
	select {
	case <-memoryReady:
	case <-ctx.Done():
		t.Fatalf("sampler did not report nonzero memory: %v", ctx.Err())
	}
	detach()
	waitForSamplerStop(t)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.samples) == 0 {
		t.Fatal("no samples captured")
	}
	for _, sm := range sink.samples {
		if sm.MemoryBytes > 0 {
			return
		}
	}
	t.Fatal("all memory readings were zero")
}
