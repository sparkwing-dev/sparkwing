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

func TestRecordProfileObservation_PlanHashChangeResetsWindow(t *testing.T) {
	st, ctx := openTestStore(t)

	for range 3 {
		if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: 5 * time.Second, PeakCores: 4, PeakMemoryBytes: 8 << 20,
			CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: 6 * time.Second, PeakCores: 1, PeakMemoryBytes: 2 << 20,
		CPUMeasured: true, PlanHash: "B",
	}); err != nil {
		t.Fatal(err)
	}

	prof, _ := st.defaultTenant().GetPipelineProfile(ctx, "demo", "")
	if prof.PlanHash != "B" {
		t.Errorf("PlanHash = %q, want B", prof.PlanHash)
	}
	if prof.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1 (window reset to the new version's first clean sample)", prof.SampleCount)
	}
	if prof.PrevPeakCores != 4 {
		t.Errorf("PrevPeakCores = %v, want 4 (predecessor peak carried for warm start)", prof.PrevPeakCores)
	}
}

func TestRecordProfileObservation_PeakIgnoresSingleOutlier(t *testing.T) {
	st, ctx := openTestStore(t)
	if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
		Duration: time.Second, PeakCores: 40, CPUMeasured: true, PlanHash: "A",
	}); err != nil {
		t.Fatal(err)
	}
	for range profileWindow - 1 {
		if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 2, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.defaultTenant().GetPipelineProfile(ctx, "demo", "")
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
		if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 10, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range profileWindow {
		if err := st.defaultTenant().RecordProfileObservation(ctx, "demo", "", ProfileObservation{
			Duration: time.Second, PeakCores: 2, CPUMeasured: true, PlanHash: "A",
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, _ := st.defaultTenant().GetPipelineProfile(ctx, "demo", "")
	if prof.SampleCount != profileWindow {
		t.Fatalf("SampleCount = %d, want %d (window bounded)", prof.SampleCount, profileWindow)
	}
	if prof.PeakCores != 2 {
		t.Errorf("PeakCores = %v, want 2 (old expensive samples aged out)", prof.PeakCores)
	}
}
