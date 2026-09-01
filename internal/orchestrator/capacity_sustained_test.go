package orchestrator

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func sustainedFixture(t *testing.T, pipeline string, millicores []int64) (*store.Store, time.Time, time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	start := time.Now()
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: pipeline, Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for i, cpu := range millicores {
		if err := st.AddNodeMetricSample(ctx, "r1", "build", store.MetricSample{
			TS:            start.Add(time.Duration(i) * 2 * time.Second),
			CPUMillicores: cpu,
			MemoryBytes:   1 << 30,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st, start, start.Add(time.Duration(len(millicores)) * 2 * time.Second)
}

func TestRecordRunProfile_SustainedIsThePlateauNotTheBurst(t *testing.T) {
	if runtime.NumCPU() < 5 {
		t.Skip("host cannot hold a five-core reading")
	}
	series := []int64{1000, 1000, 1000, 1000, 5000, 1000, 1000, 1000, 1000, 1000}
	st, start, end := sustainedFixture(t, "spiky", series)
	ctx := context.Background()

	recordRunProfile(ctx, st, "spiky", "r1", nil, "", runCharge{}, false, start, end)

	rollup, err := st.GetPipelineProfile(ctx, "spiky", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.SustainedCores != 1.4 {
		t.Errorf("rollup SustainedCores = %v, want 1.4 (the mean guard: the hot tick joins the average, not the plateau rank)", rollup.SustainedCores)
	}
	if rollup.PeakCores != 5.0 {
		t.Errorf("rollup PeakCores = %v, want 5.0 still recorded", rollup.PeakCores)
	}
	node, err := st.GetPipelineProfile(ctx, "spiky", "build")
	if err != nil || node == nil {
		t.Fatalf("node profile missing: %v", err)
	}
	if node.SustainedCores != 1.4 {
		t.Errorf("node SustainedCores = %v, want 1.4", node.SustainedCores)
	}
	if node.PeakCores != 5.0 {
		t.Errorf("node PeakCores = %v, want 5.0", node.PeakCores)
	}
}

func TestRecordRunProfile_ShortRunSustainedIsItsMaximum(t *testing.T) {
	if runtime.NumCPU() < 3 {
		t.Skip("host cannot hold a three-core reading")
	}
	for _, tc := range []struct {
		name   string
		series []int64
	}{
		{"one interval", []int64{3000}},
		{"two intervals", []int64{1000, 3000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, start, end := sustainedFixture(t, "short", tc.series)
			ctx := context.Background()

			recordRunProfile(ctx, st, "short", "r1", nil, "", runCharge{}, false, start, end)

			rollup, err := st.GetPipelineProfile(ctx, "short", "")
			if err != nil || rollup == nil {
				t.Fatalf("rollup profile missing: %v", err)
			}
			if rollup.SustainedCores != 3.0 || rollup.PeakCores != 3.0 {
				t.Errorf("rollup sustained/peak = %v/%v, want both 3.0",
					rollup.SustainedCores, rollup.PeakCores)
			}
		})
	}
}

func TestRecordRunProfile_OneShotSamplesJoinTheWindowTheyLandIn(t *testing.T) {
	if runtime.NumCPU() < 5 {
		t.Skip("host cannot hold a five-core reading")
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
	for _, nodeID := range []string{"fan-a", "fan-b"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			if err := st.AddNodeMetricSample(ctx, "r1", nodeID, store.MetricSample{
				TS:            start.Add(time.Duration(i) * 2 * time.Second),
				CPUMillicores: 500,
				MemoryBytes:   1 << 30,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.AddNodeMetricSample(ctx, "r1", "fan-a", store.MetricSample{
		TS:            start.Add(8*time.Second + time.Millisecond),
		CPUMillicores: 4000,
	}); err != nil {
		t.Fatal(err)
	}

	recordRunProfile(ctx, st, "fan", "r1", nil, "", runCharge{}, false, start, start.Add(10*time.Second))

	rollup, err := st.GetPipelineProfile(ctx, "fan", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.PeakCores != 5.0 {
		t.Errorf("rollup PeakCores = %v, want 5.0 (the one-shot's four cores plus the two halves beside it)", rollup.PeakCores)
	}
	if rollup.SustainedCores != 1.8 {
		t.Errorf("rollup SustainedCores = %v, want 1.8 (four windows at one core and one at five)", rollup.SustainedCores)
	}
}

func TestRecordRunProfile_ContendedFloorResistsContentionDeflation(t *testing.T) {
	if runtime.NumCPU() < 4 {
		t.Skip("host cannot hold a four-core reading")
	}
	series := []int64{1000, 1000, 1000, 1000, 4000, 1000, 1000, 1000, 1000, 1000}
	st, start, end := sustainedFixture(t, "spiky", series)
	ctx := context.Background()

	recordRunProfile(ctx, st, "spiky", "r1", nil, "A", runCharge{Cores: 4}, true, start, end)

	rollup, err := st.GetPipelineProfile(ctx, "spiky", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.FloorCores != 4.0 {
		t.Errorf("FloorCores = %v, want 4.0 (the peak hit the ceiling; the 1.3 sustained level must not price the floor)",
			rollup.FloorCores)
	}
}

func TestRecordRunProfile_ContendedBelowCeilingStillFloorsAtItsPeak(t *testing.T) {
	if runtime.NumCPU() < 4 {
		t.Skip("host cannot hold a four-core reading")
	}
	series := []int64{1000, 1000, 1000, 1000, 2000, 1000, 1000, 1000, 1000, 1000}
	st, start, end := sustainedFixture(t, "spiky", series)
	ctx := context.Background()

	recordRunProfile(ctx, st, "spiky", "r1", nil, "A", runCharge{Cores: 8}, true, start, end)

	rollup, err := st.GetPipelineProfile(ctx, "spiky", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup profile missing: %v", err)
	}
	if rollup.FloorCores != 2.0 {
		t.Errorf("FloorCores = %v, want its 2.0 peak (no ceiling escalation at a charge of 8)", rollup.FloorCores)
	}
}
