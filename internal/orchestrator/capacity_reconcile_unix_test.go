//go:build unix

package orchestrator

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type reconcileSink struct {
	st            *store.Store
	runID, nodeID string
	samples       chan<- nodemetrics.Sample
}

func (s reconcileSink) Push(ctx context.Context, sm nodemetrics.Sample) error {
	if err := s.st.AddNodeMetricSample(ctx, s.runID, s.nodeID, store.MetricSample{
		TS:   sm.TS,
		Kind: store.MetricInterval, CPUAvailable: sm.CPUAvailable, MemoryAvailable: sm.MemoryAvailable,
		CPUMillicores: sm.CPUMillicores,
		MemoryBytes:   sm.MemoryBytes,
	}); err != nil {
		return err
	}
	if s.samples != nil {
		select {
		case s.samples <- sm:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestRecordRunProfile_KnownChildSamplingGapCannotQualify(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.5s of real work; the fast class runs under -short")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no CPU accounting on this platform")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "burn", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "step", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(nodemetrics.SetIntervalForTest(200 * time.Millisecond))
	sampCtx, stopSampler := context.WithCancel(ctx)
	samples := make(chan nodemetrics.Sample, 4)
	detachSampler := nodemetrics.Attach(sampCtx, reconcileSink{st: st, runID: "r1", nodeID: "step", samples: samples}, true)
	var stopOnce sync.Once
	stopSampling := func() {
		stopOnce.Do(func() {
			detachSampler()
			stopSampler()
		})
	}
	t.Cleanup(stopSampling)

	startedAt := time.Now()
	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start burner: %v", err)
	}
	burnerWaited := false
	t.Cleanup(func() {
		if !burnerWaited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForReconcileSampleAfter(t, sampCtx, samples, time.Time{})
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill burner: %v", err)
	}
	_ = cmd.Wait()
	burnerWaited = true
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

	if err := st.AddNodeMetricSample(ctx, "r1", "step", store.MetricSample{
		TS:            time.Now(),
		CPUMillicores: trueMillicores,
		Kind:          store.MetricCommand, CPUAvailable: true, CPUTime: childCPU,
	}); err != nil {
		t.Fatal(err)
	}

	reportedAt := time.Now()
	waitForReconcileSampleAfter(t, sampCtx, samples, reportedAt)
	stopSampling()

	recordRunProfile(ctx, localState{st: st}, "burn", "r1", nil, "", runCharge{}, false, start, time.Now())

	rollup, err := st.GetPipelineProfile(ctx, "burn", "")
	if err != nil {
		t.Fatal(err)
	}
	if rollup != nil {
		t.Fatalf("known child sampling gap became an admission profile: %+v", rollup)
	}
	records, err := st.ListNodeMetrics(ctx, "r1", "step")
	if err != nil {
		t.Fatal(err)
	}
	var retained time.Duration
	for _, sample := range records {
		if sample.Kind == store.MetricCommand {
			retained += sample.CPUTime
		}
	}
	if retained != childCPU {
		t.Fatalf("command CPU=%s want kernel total %s", retained, childCPU)
	}
}

func waitForReconcileSampleAfter(t *testing.T, ctx context.Context, samples <-chan nodemetrics.Sample, after time.Time) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case sample := <-samples:
			if after.IsZero() || sample.TS.After(after) {
				return
			}
		case <-waitCtx.Done():
			t.Fatalf("node-metrics sampler did not persist a sample after %s: %v", after, waitCtx.Err())
		}
	}
}
