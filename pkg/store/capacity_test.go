package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPipelineProfile_RoundTripsPercentilesAndPeaks(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for _, obs := range []store.ProfileObservation{
		{Duration: 10 * time.Second, PeakCores: 2.0, PeakMemoryBytes: 1 << 30},
		{Duration: 20 * time.Second, PeakCores: 4.0, PeakMemoryBytes: 2 << 30},
		{Duration: 30 * time.Second, PeakCores: 8.0, PeakMemoryBytes: 3 << 30},
	} {
		if err := st.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
			t.Fatalf("RecordProfileObservation: %v", err)
		}
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof == nil {
		t.Fatal("profile is nil after three observations")
	}
	if prof.SampleCount != 3 {
		t.Errorf("SampleCount = %d, want 3", prof.SampleCount)
	}
	if prof.P50Duration != 20*time.Second {
		t.Errorf("P50Duration = %s, want 20s", prof.P50Duration)
	}
	if prof.P99Duration != 30*time.Second {
		t.Errorf("P99Duration = %s, want 30s", prof.P99Duration)
	}
	if prof.PeakCores != 8.0 {
		t.Errorf("PeakCores = %v, want 8", prof.PeakCores)
	}
	if prof.PeakMemoryBytes != 3<<30 {
		t.Errorf("PeakMemoryBytes = %d, want %d", prof.PeakMemoryBytes, 3<<30)
	}
	if prof.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestPipelineProfile_AbsentReturnsNil(t *testing.T) {
	st := openTestStore(t)
	prof, err := st.GetPipelineProfile(context.Background(), "never-run", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Fatalf("expected nil profile, got %+v", prof)
	}
}

func TestPipelineProfile_WindowAgesOutOldSamples(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 80; i++ {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			Duration:  time.Duration(i) * time.Second,
			PeakCores: float64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof.SampleCount != 20 {
		t.Errorf("SampleCount = %d, want capped at the window size", prof.SampleCount)
	}
	if prof.PeakCores < 70 {
		t.Errorf("PeakCores = %v, want a recent-window value", prof.PeakCores)
	}
}

func TestPipelineProfile_ListReturnsRollupAndNodeRows(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	obs := store.ProfileObservation{Duration: time.Second, PeakCores: 1}
	if err := st.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProfileObservation(ctx, "demo", "build", obs); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProfileObservation(ctx, "other", "", obs); err != nil {
		t.Fatal(err)
	}

	demo, err := st.ListPipelineProfiles(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(demo) != 2 {
		t.Fatalf("demo profiles = %d, want 2", len(demo))
	}
	if demo[0].NodeID != "" || demo[1].NodeID != "build" {
		t.Errorf("rollup should sort first: %+v", demo)
	}

	all, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all profiles = %d, want 3", len(all))
	}
}
