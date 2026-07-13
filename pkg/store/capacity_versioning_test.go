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

// TestRecordProfileObservation_ContendedRaisesFloorNotPeak pins BW-690: a
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

// TestRecordProfileObservation_FloorOnlyRises confirms the floor is a
// monotone lower bound within a version: a smaller later contended reading
// never lowers it.
func TestRecordProfileObservation_FloorOnlyRises(t *testing.T) {
	st, ctx := openTestStore(t)
	for _, c := range []float64{5, 2, 7, 3} {
		if err := st.RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			CPUMeasured: true, PlanHash: "A", Contended: true, FloorCores: c,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.GetPipelineProfile(ctx, "demo", "")
	if prof.FloorCores != 7 {
		t.Errorf("FloorCores = %v, want 7 (highest contended lower bound)", prof.FloorCores)
	}
}

// TestRecordProfileObservation_PlanHashChangeResetsWindow pins BW-693: a
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
