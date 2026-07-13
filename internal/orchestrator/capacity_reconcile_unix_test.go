//go:build unix

package orchestrator

import (
	"context"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type reconcileSink struct {
	st            *store.Store
	runID, nodeID string
}

func (s reconcileSink) Push(ctx context.Context, sm nodemetrics.Sample) error {
	return s.st.AddNodeMetricSample(ctx, s.runID, s.nodeID, store.MetricSample{
		TS:            sm.TS,
		CPUMillicores: sm.CPUMillicores,
		MemoryBytes:   sm.MemoryBytes,
	})
}

// TestRecordRunProfile_SDKBurnerPeakNotDoubled runs the sampler alongside a
// reaped burner whose CPU is reported through the per-command path, as a real
// node does. Without reconciliation the child's usage would land in both the
// sampler's RUSAGE_CHILDREN spike and the per-command report, inflating the
// folded peak past its true burn; the subtraction keeps the peak near truth.
func TestRecordRunProfile_SDKBurnerPeakNotDoubled(t *testing.T) {
	if !nodemetrics.CPUAccountingAvailable() {
		t.Skip("no CPU accounting on this platform")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "burn", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "step", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	sampCtx, stopSampler := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		nodemetrics.Run(sampCtx, 200*time.Millisecond, reconcileSink{st: st, runID: "r1", nodeID: "step"})
		close(done)
	}()

	startedAt := time.Now()
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start burner: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	wall := time.Since(startedAt)

	ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		t.Skip("no wait4 rusage on this platform")
	}
	childCPU := time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
	trueMillicores := int64(childCPU.Seconds() / wall.Seconds() * 1000.0)
	if trueMillicores < 300 {
		t.Skipf("burner drew only %d millicores; host too loaded to measure", trueMillicores)
	}

	nodemetrics.AddReportedChildCPU(childCPU)
	if err := st.AddNodeMetricSample(ctx, "r1", "step", store.MetricSample{
		TS:            time.Now(),
		CPUMillicores: trueMillicores,
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(220 * time.Millisecond)
	stopSampler()
	<-done

	recordRunProfile(ctx, st, "burn", "r1", nil, "", runCharge{}, false, start, time.Now())

	rollup, err := st.GetPipelineProfile(ctx, "burn", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	trueCores := float64(trueMillicores) / 1000.0
	if rollup.PeakCores > trueCores*1.3 {
		t.Errorf("peak cores = %.3f, want <= %.3f (1.3x true burn %.3f) -- child double-counted",
			rollup.PeakCores, trueCores*1.3, trueCores)
	}
	if rollup.PeakCores < trueCores*0.7 {
		t.Errorf("peak cores = %.3f, want >= %.3f -- per-command report lost, burn undercounted",
			rollup.PeakCores, trueCores*0.7)
	}
}
