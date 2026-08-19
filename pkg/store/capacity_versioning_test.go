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

// TestRecordProfileObservation_ContendedRaisesFloorNotPeak verifies that a
// contended observation feeds the demand floor only. It never enters the
// clean window, so the measured peak, the duration percentiles, and the
// sample count that graduates a version are all untouched.
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

// TestRecordProfileObservation_FloorRisesOnHigherEvidence confirms contended
// evidence at or above the stored floor raises it outright.
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

// TestRecordProfileObservation_FloorDecaysTowardLowerEvidence verifies that
// lower evidence decays an inflated floor
// by half per run -- never past the run's own evidence -- so a floor ratcheted
// up by transient external load converges back to demand instead of pricing
// the pipeline at the machine ceiling until a manual reset.
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

	// Evidence between half the floor and the floor stops the halving short:
	// unclamped it would land on 4 and price the pipeline below the 5 cores
	// this run proved it wanted.
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

// TestRecordProfileObservation_PlanHashChangeResetsWindow verifies that a
// structural change clears the version's learned window and floor and carries
// the outgoing peak into PrevPeak, so the changed version re-measures from a
// warm start rather than inheriting stale samples.
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

// TestRecordProfileObservation_PeakIgnoresSingleOutlier confirms the charged
// peak is the window's p95, so one freak run cannot pin the price for the
// whole window.
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

// TestRecordProfileObservation_WindowAgesOutOldCost confirms an old
// expensive generation stops influencing the charge once profileWindow
// cheaper runs have folded in.
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
