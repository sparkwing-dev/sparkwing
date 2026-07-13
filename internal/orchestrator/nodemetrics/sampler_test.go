package nodemetrics

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type captureSink struct {
	mu      sync.Mutex
	samples []Sample
}

func (s *captureSink) Push(_ context.Context, sample Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
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

// TestRun_ReportsNonzeroCPUUnderLoad: on the host platform a
// CPU-burning process must produce a nonzero sampled peak, so learned
// capacity can activate rather than costing every run by the default.
func TestRun_ReportsNonzeroCPUUnderLoad(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 40*time.Millisecond, sink)
		close(done)
	}()

	burnUntil := time.Now().Add(600 * time.Millisecond)
	x := 0
	for time.Now().Before(burnUntil) {
		x++
		_ = x * x
	}
	cancel()
	<-done

	if peak := sink.peakCPU(); peak <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 after burning a core", peak)
	}
}

// TestRun_CountsRawExecChildrenCPU verifies that CPU burned by a child
// spawned with os/exec outside the SDK command wrapper surfaces in the
// sampled peak through RUSAGE_CHILDREN, so a raw-exec pipeline cannot measure
// zero and be over-admitted at the floor. The parent stays near idle while
// each child burns, so the peak reflects the children rather than self.
func TestRun_CountsRawExecChildrenCPU(t *testing.T) {
	if _, ok := readCPUTime(); !ok {
		t.Skip("no CPU accounting on this platform")
	}
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 40*time.Millisecond, sink)
		close(done)
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		cmd := exec.Command("sh", "-c", "while :; do :; done")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start burner: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	cancel()
	<-done

	if peak := sink.peakCPU(); peak <= 300 {
		t.Fatalf("peak CPU millicores = %d, want > 300 from raw-exec child burn", peak)
	}
}

// TestReadCPUTime_SubtractsReportedChildCPU pins the reconciliation: a reaped
// child raises the cumulative reading through RUSAGE_CHILDREN, and reporting
// its CPU (as the SDK per-command path does) brings the reading back down, so
// the same usage is not counted twice.
func TestReadCPUTime_SubtractsReportedChildCPU(t *testing.T) {
	base, ok := readCPUTime()
	if !ok {
		t.Skip("no CPU accounting on this platform")
	}
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start burner: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	withChild, _ := readCPUTime()
	childCPU := withChild - base
	if childCPU < 100*time.Millisecond {
		t.Fatalf("reaped child raised reading by %s, want a clear burn via RUSAGE_CHILDREN", childCPU)
	}

	AddReportedChildCPU(childCPU)
	reconciled, _ := readCPUTime()
	if reconciled >= withChild-childCPU/2 {
		t.Fatalf("reading after reporting = %s, want the child's %s subtracted back out", reconciled, childCPU)
	}
}

// TestRun_ReportsMemory asserts the sampler reports a nonzero memory
// reading from the platform RSS source or its runtime fallback.
func TestRun_ReportsMemory(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 40*time.Millisecond, sink)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

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
