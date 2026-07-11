package orchestrator

import (
	"context"
	"path/filepath"
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
	recordRunProfile(ctx, st, "demo", "r1", pin, start, start.Add(6*time.Second))

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
	recordRunProfile(ctx, st, "demo", "r1", nil, start, start.Add(time.Second))

	if rollup, _ := st.GetPipelineProfile(ctx, "demo", ""); rollup != nil {
		t.Errorf("expected no profile for a run with no samples, got %+v", rollup)
	}
}
