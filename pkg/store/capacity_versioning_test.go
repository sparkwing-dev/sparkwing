package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, context.Background()
}

func TestRecordProfileObservation_ContendedRaisesFloorNotPeak(t *testing.T) {
	st, ctx := openTestStore(t)

	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: 10 * time.Second, PeakCores: 2, PeakMemoryBytes: 1 << 20,
		CPUMeasured: true, PlanHash: "A",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		CPUMeasured: true, PlanHash: "A", Contended: true,
		FloorCores: 5, FloorMemoryBytes: 4 << 20,
	}); err != nil {
		t.Fatal(err)
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil || prof == nil {
		t.Fatalf("profile missing: %v", err)
	}
	if prof.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1 (contended run does not graduate)", prof.SampleCount)
	}
	if prof.PeakCores != 2 {
		t.Errorf("PeakCores = %v, want 2 (contended run does not set the peak)", prof.PeakCores)
	}
	if prof.FloorCores != 5 {
		t.Errorf("FloorCores = %v, want 5", prof.FloorCores)
	}
	if prof.FloorMemoryBytes != 4<<20 {
		t.Errorf("FloorMemoryBytes = %d, want %d", prof.FloorMemoryBytes, 4<<20)
	}
}

func TestRecordProfileObservation_FloorRisesOnHigherEvidence(t *testing.T) {
	st, ctx := openTestStore(t)
	for _, c := range []float64{5, 7} {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			CPUMeasured: true, PlanHash: "A", Contended: true, FloorCores: c,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.FloorCores != 7 {
		t.Errorf("FloorCores = %v, want 7 (higher contended evidence raises the floor)", prof.FloorCores)
	}
}

func TestRecordProfileObservation_FloorDecaysTowardLowerEvidence(t *testing.T) {
	st, ctx := openTestStore(t)
	fold := func(cores float64, memBytes int64) {
		t.Helper()
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			CPUMeasured: true, PlanHash: "A", Contended: true,
			FloorCores: cores, FloorMemoryBytes: memBytes,
		}); err != nil {
			t.Fatal(err)
		}
	}

	fold(8, 8<<30)
	fold(1, 1<<30)
	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.FloorCores != 4 {
		t.Errorf("FloorCores = %v, want 4 (one under-floor run halves the floor)", prof.FloorCores)
	}
	if prof.FloorMemoryBytes != 4<<30 {
		t.Errorf("FloorMemoryBytes = %d, want %d (memory floor decays alongside cores)", prof.FloorMemoryBytes, int64(4<<30))
	}

	fold(1, 1<<30)
	fold(1, 1<<30)
	prof, _ = st.GetPipelineProfile(ctx, "demo", "")
	if prof.FloorCores != 1 {
		t.Errorf("FloorCores = %v, want 1 (three halvings reach the run's own evidence)", prof.FloorCores)
	}
	if prof.FloorMemoryBytes != 1<<30 {
		t.Errorf("FloorMemoryBytes = %d, want %d (memory reaches its evidence alongside cores)", prof.FloorMemoryBytes, int64(1<<30))
	}

	fold(8, 8<<30)
	fold(5, 5<<30)
	prof, _ = st.GetPipelineProfile(ctx, "demo", "")
	if prof.FloorCores != 5 {
		t.Errorf("FloorCores = %v, want 5 (decay never falls below the run's own evidence)", prof.FloorCores)
	}
	if prof.FloorMemoryBytes != 5<<30 {
		t.Errorf("FloorMemoryBytes = %d, want %d (decay never falls below the run's own evidence)", prof.FloorMemoryBytes, int64(5<<30))
	}
}

func TestRecordProfileObservation_PlanHashChangeResetsWindow(t *testing.T) {
	st, ctx := openTestStore(t)

	for range 3 {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: 5 * time.Second, PeakCores: 4, PeakMemoryBytes: 8 << 20,
			CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		CPUMeasured: true, PlanHash: "A", Contended: true, FloorCores: 9,
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: 6 * time.Second, PeakCores: 1, PeakMemoryBytes: 2 << 20,
		CPUMeasured: true, PlanHash: "B",
	}); err != nil {
		t.Fatal(err)
	}

	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.PlanHash != "B" {
		t.Errorf("PlanHash = %q, want B", prof.PlanHash)
	}
	if prof.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1 (window reset to the new version's first clean sample)", prof.SampleCount)
	}
	if prof.PrevPeakCores != 4 {
		t.Errorf("PrevPeakCores = %v, want 4 (predecessor peak carried for warm start)", prof.PrevPeakCores)
	}
	if prof.FloorCores != 0 {
		t.Errorf("FloorCores = %v, want 0 (predecessor floor does not carry to the new version)", prof.FloorCores)
	}
}

func TestRecordProfileObservation_PeakIgnoresSingleOutlier(t *testing.T) {
	st, ctx := openTestStore(t)
	if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: time.Second, PeakCores: 40, CPUMeasured: true, PlanHash: "A",
	}); err != nil {
		t.Fatal(err)
	}
	for range profileWindow - 1 {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 2, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.SampleCount != profileWindow {
		t.Fatalf("SampleCount = %d, want %d", prof.SampleCount, profileWindow)
	}
	if prof.PeakCores != 2 {
		t.Errorf("PeakCores = %v, want 2 (p95 drops the single outlier)", prof.PeakCores)
	}
}

func TestRecordProfileObservation_WindowAgesOutOldCost(t *testing.T) {
	st, ctx := openTestStore(t)
	for range 5 {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 10, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range profileWindow {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 2, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.SampleCount != profileWindow {
		t.Fatalf("SampleCount = %d, want %d (window bounded)", prof.SampleCount, profileWindow)
	}
	if prof.PeakCores != 2 {
		t.Errorf("PeakCores = %v, want 2 (old expensive samples aged out)", prof.PeakCores)
	}
}
