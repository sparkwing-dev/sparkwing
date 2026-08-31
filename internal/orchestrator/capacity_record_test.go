package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRecordRunProfile_AggregatesNodeMetricsIntoProfiles(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for i, cpu := range []int64{500, 2000, 1500} {
		if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
			TS:            start.Add(time.Duration(i) * 2 * time.Second),
			CPUMillicores: cpu,
			MemoryBytes:   int64(i+1) << 30,
		}); err != nil {
			t.Fatal(err)
		}
	}

	pin := &capacity.Pin{Cores: 1}
	recordRunProfile(ctx, st, "demo", "r1", pin, "", runCharge{}, false, start, start.Add(6*time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.PeakCores != 2.0 {
		t.Errorf("rollup PeakCores = %v, want 2.0 (max sample 2000 millicores)", rollup.PeakCores)
	}
	if rollup.SampleCount != 1 {
		t.Errorf("rollup SampleCount = %d, want 1", rollup.SampleCount)
	}
	if rollup.PinnedCores != 1 {
		t.Errorf("rollup PinnedCores = %v, want the persisted pin 1", rollup.PinnedCores)
	}

	node, err := st.GetPipelineProfile(ctx, "demo", "build")
	if err != nil || node == nil {
		t.Fatalf("node profile missing: %v", err)
	}
	if node.PeakMemoryBytes != 3<<30 {
		t.Errorf("node PeakMemoryBytes = %d, want %d", node.PeakMemoryBytes, 3<<30)
	}
}

func TestRecordRunProfile_ContendedCeilingHitEscalatesFloor(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: start, CPUMillicores: 4000, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "demo", "r1", nil, "A", runCharge{Cores: 4}, true, start, start.Add(time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.FloorCores != 4 {
		t.Errorf("FloorCores = %v, want 4 (ceiling hit escalates the floor to the charge)", rollup.FloorCores)
	}
	if rollup.SampleCount != 0 {
		t.Errorf("SampleCount = %d, want 0 (contended run does not graduate)", rollup.SampleCount)
	}
	if rollup.PeakCores != 0 {
		t.Errorf("PeakCores = %v, want 0 (contended run sets no measured peak)", rollup.PeakCores)
	}
}

func TestRecordRunProfile_ContendedBelowCeilingSetsFloorOnly(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: start, CPUMillicores: 1000, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "demo", "r1", nil, "A", runCharge{Cores: 8}, true, start, start.Add(time.Second))

	rollup, _ := st.GetPipelineProfile(ctx, "demo", "")
	if rollup.FloorCores != 1 {
		t.Errorf("FloorCores = %v, want 1 (measured peak; no ceiling escalation)", rollup.FloorCores)
	}
}

func TestRecordRunProfile_CapsCPUProfileAtHostCapacity(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS:            start,
		CPUMillicores: int64(runtime.NumCPU()+4) * 1000,
		MemoryBytes:   1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "demo", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if want := float64(runtime.NumCPU()); rollup.PeakCores != want {
		t.Errorf("rollup PeakCores = %v, want host capacity %v", rollup.PeakCores, want)
	}
	node, err := st.GetPipelineProfile(ctx, "demo", "build")
	if err != nil || node == nil {
		t.Fatalf("node profile missing: %v", err)
	}
	if want := float64(runtime.NumCPU()); node.PeakCores != want {
		t.Errorf("node PeakCores = %v, want host capacity %v", node.PeakCores, want)
	}
}

func TestRecordRunProfile_RollupSumsTheSharesOneIntervalWasSplitInto(t *testing.T) {
	if runtime.NumCPU() < 3 {
		t.Skip("host cannot hold a three-core reading")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now().Truncate(nodemetrics.Interval())
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "fan", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	ticks := []struct {
		ts    time.Time
		share store.MetricSample
	}{
		{ts: start, share: store.MetricSample{CPUMillicores: 500, MemoryBytes: 1 << 30}},
		{ts: start.Add(2 * time.Second), share: store.MetricSample{CPUMillicores: 1000, MemoryBytes: 2 << 30}},
	}
	for _, nodeID := range []string{"fan-a", "fan-b"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		for _, tick := range ticks {
			sample := tick.share
			sample.TS = tick.ts
			if err := st.AddNodeMetricSample(ctx, "r1", nodeID, sample); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "fan-a", store.MetricSample{
		TS:            start.Add(2*time.Second + time.Millisecond),
		CPUMillicores: 300,
	}); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "fan", "r1", nil, "", runCharge{}, false, start, start.Add(4*time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "fan", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.PeakCores != 2.3 {
		t.Errorf("rollup PeakCores = %v, want 2.3 (both nodes' readings plus the one-shot drawn beside them)", rollup.PeakCores)
	}
	if rollup.PeakMemoryBytes != 4<<30 {
		t.Errorf("rollup PeakMemoryBytes = %d, want %d (both halves of the heaviest tick)", rollup.PeakMemoryBytes, int64(4)<<30)
	}

	for _, nodeID := range []string{"fan-a", "fan-b"} {
		node, err := st.GetPipelineProfile(ctx, "fan", nodeID)
		if err != nil || node == nil {
			t.Fatalf("node %q profile missing: %v", nodeID, err)
		}
		if node.PeakCores != 1.0 {
			t.Errorf("node %q PeakCores = %v, want its own 1.0 share", nodeID, node.PeakCores)
		}
		if node.PeakMemoryBytes != 2<<30 {
			t.Errorf("node %q PeakMemoryBytes = %d, want its own %d share", nodeID, node.PeakMemoryBytes, int64(2)<<30)
		}
	}
}

func TestRecordRunProfile_ClearsStoredPinWhenPlanDeclaresNone(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	createMeasuredRun := func(runID string) {
		t.Helper()
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddNodeMetricSample(ctx, runID, "build", store.MetricSample{
			TS:            start,
			CPUMillicores: 1200,
			MemoryBytes:   1 << 30,
		}); err != nil {
			t.Fatal(err)
		}
	}

	createMeasuredRun("r1")
	recordRunProfile(ctx, st, "demo", "r1", &capacity.Pin{Cores: 0.25}, "", runCharge{}, false, start, start.Add(time.Second))
	createMeasuredRun("r2")
	recordRunProfile(ctx, st, "demo", "r2", nil, "", runCharge{}, false, start, start.Add(time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.PinnedCores != 0 || rollup.PinnedMemoryBytes != 0 {
		t.Fatalf("rollup pin = %.2f cores/%d bytes, want cleared after undeclared plan", rollup.PinnedCores, rollup.PinnedMemoryBytes)
	}
}

func TestRecordRunProfile_DurationExcludesQueueWait(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	submit := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: submit}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: submit.Add(10 * time.Second), CPUMillicores: 1000, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	queueWait := 10 * time.Second
	execTime := 10 * time.Second
	execStart := submit.Add(queueWait)
	recordRunProfile(ctx, st, "demo", "r1", nil, "", runCharge{}, false, execStart, execStart.Add(execTime))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if got := rollup.P50Duration; got < 9*time.Second || got > 11*time.Second {
		t.Errorf("rollup P50Duration = %s, want ~%s (execution only, not %s incl. queue wait)",
			got, execTime, queueWait+execTime)
	}
}

func TestRecordRunProfile_CacheDominantRunsKeepPercentilesAndPeaks(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	start := time.Now()

	measured := func(runID string) {
		t.Helper()
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddNodeMetricSample(ctx, runID, "build", store.MetricSample{
			TS: start, CPUMillicores: 3000, MemoryBytes: 2 << 30,
		}); err != nil {
			t.Fatal(err)
		}
		recordRunProfile(ctx, st, "demo", runID, nil, "", runCharge{}, false, start, start.Add(30*time.Second))
	}
	cachedRun := func(runID string) {
		t.Helper()
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "build", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, runID, "build", string(sparkwing.Cached), "", nil); err != nil {
			t.Fatal(err)
		}
		recordRunProfile(ctx, st, "demo", runID, nil, "", runCharge{}, false, start, start.Add(41*time.Millisecond))
	}

	measured("r1")
	measured("r2")
	measured("r3")
	before, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || before == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}

	for i := 0; i < 5; i++ {
		cachedRun(fmt.Sprintf("c%d", i))
	}
	after, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || after == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}

	if after.SampleCount != before.SampleCount {
		t.Errorf("SampleCount moved from %d to %d after a cached burst", before.SampleCount, after.SampleCount)
	}
	if after.PeakCores != before.PeakCores {
		t.Errorf("PeakCores moved from %v to %v after a cached burst", before.PeakCores, after.PeakCores)
	}
	if after.P50Duration != before.P50Duration {
		t.Errorf("P50Duration moved from %s to %s after a cached burst", before.P50Duration, after.P50Duration)
	}

	counts, err := st.CacheExcludedCounts(ctx, "demo", string(sparkwing.Cached), capacity.CacheDominantFraction)
	if err != nil {
		t.Fatalf("CacheExcludedCounts: %v", err)
	}
	if counts["demo"] != 5 {
		t.Errorf("cache-excluded count = %d, want 5", counts["demo"])
	}
}

// TestRecordRunProfile_CacheDominantRunStillFoldsItsExecutedNodes is the
// BW-1878 acceptance criterion: a merge-gate-shaped run where nearly every
// node cache-hits must still fold the nodes that executed, or those nodes
// never reach MinSamples and are charged the still-measuring price forever.
// The run-level rollup stays excluded -- its wall time measured the cache.
func TestRecordRunProfile_CacheDominantRunStillFoldsItsExecutedNodes(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("capLocalPeakCores caps the asserted peak at host cores")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	start := time.Now()

	if err := st.CreateRun(ctx, store.Run{ID: "land1", Pipeline: "gate", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		nodeID := fmt.Sprintf("cached-%d", i)
		if err := st.CreateNode(ctx, store.Node{RunID: "land1", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, "land1", nodeID, string(sparkwing.Cached), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "land1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "land1", "build", store.MetricSample{
		TS: start, CPUMillicores: 2000, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "land1", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "gate", "land1", nil, "", runCharge{}, false, start, start.Add(20*time.Second))

	node, err := st.GetPipelineProfile(ctx, "gate", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node == nil || node.SampleCount != 1 {
		t.Fatalf("executed node profile = %+v, want one folded sample", node)
	}
	if node.PeakCores < 1.9 || node.PeakCores > 2.1 {
		t.Errorf("executed node PeakCores = %v, want ~2.0", node.PeakCores)
	}
	rollup, err := st.GetPipelineProfile(ctx, "gate", "")
	if err != nil {
		t.Fatal(err)
	}
	if rollup != nil {
		t.Errorf("rollup profile = %+v, want none: a cache-dominant run's wall time measured the cache", rollup)
	}
}

func TestRecordRunProfile_MixedRunBelowThresholdStillFolds(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	start := time.Now()

	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "fetch", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
		TS: start, CPUMillicores: 2000, MemoryBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "r1", "build", string(sparkwing.Success), "", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNode(ctx, "r1", "fetch", string(sparkwing.Cached), "", nil); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "demo", "r1", nil, "", runCharge{}, false, start, start.Add(20*time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing for a below-threshold mixed run: %v", err)
	}
	if rollup.SampleCount != 1 || rollup.PeakCores != 2.0 {
		t.Errorf("rollup = samples %d peak %v, want 1 sample and 2.0 peak from the executed node",
			rollup.SampleCount, rollup.PeakCores)
	}
}

func TestRecordRunProfile_NoSamplesWritesNothing(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "quick", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	recordRunProfile(ctx, st, "demo", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))

	if rollup, _ := st.GetPipelineProfile(ctx, "demo", ""); rollup != nil {
		t.Errorf("expected no profile for a run with no samples, got %+v", rollup)
	}
}
