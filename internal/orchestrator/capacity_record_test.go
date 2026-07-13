package orchestrator

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// TestRecordRunProfile_ContendedCeilingHitEscalatesFloor pins the BW-693
// escalation: a contended run that consumed essentially its whole charge
// proves it wanted at least that much, so the demand floor rises to the
// charge (not merely the throttled measured peak), and the clean window is
// left untouched so contention never graduates the version.
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

// TestRecordRunProfile_ContendedBelowCeilingSetsFloorOnly confirms a
// contended run that stayed well under its charge only raises the floor to
// its measured peak; it does not escalate to the charge.
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

// TestRecordRunProfile_DurationExcludesQueueWait pins BW-652: the rollup
// duration measures grant-to-finish (execStart..execEnd), so a run that
// waited in admission before executing records only its execution time.
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
