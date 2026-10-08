package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPipelineProfile_RoundTripsPercentilesAndPeaks(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	for _, obs := range []store.ProfileObservation{
		{CPUMeasured: true, Duration: 10 * time.Second, PeakCores: 2.0, PeakMemoryBytes: 1 << 30},
		{CPUMeasured: true, Duration: 20 * time.Second, PeakCores: 4.0, PeakMemoryBytes: 2 << 30},
		{CPUMeasured: true, Duration: 30 * time.Second, PeakCores: 8.0, PeakMemoryBytes: 3 << 30},
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
	st := storetest.Open(t)
	prof, err := st.GetPipelineProfile(context.Background(), "never-run", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Fatalf("expected nil profile, got %+v", prof)
	}
}

func TestPipelineProfile_WindowAgesOutOldSamples(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	for i := 0; i < 80; i++ {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			CPUMeasured: true,
			Duration:    time.Duration(i) * time.Second,
			PeakCores:   float64(i),
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
	st := storetest.Open(t)
	ctx := context.Background()

	obs := store.ProfileObservation{CPUMeasured: true, Duration: time.Second, PeakCores: 1}
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

func TestPartialProfileObservationOnlyRaisesResources(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	seed := store.ProfileObservation{CPUMeasured: true, Duration: time.Minute, PeakCores: 4, SustainedCores: 2, PeakMemoryBytes: 1000}
	for range 20 {
		if err := st.RecordProfileObservation(ctx, "partial", "", seed); err != nil {
			t.Fatal(err)
		}
	}
	before, err := st.ProfileSamples(ctx, "partial", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, obs := range []store.ProfileObservation{
		{CPUMeasured: true, Partial: true, Duration: time.Second, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 10},
		{CPUMeasured: true, Partial: true, Duration: time.Second, PeakCores: 4, SustainedCores: 2, PeakMemoryBytes: 1000},
	} {
		if err := st.RecordProfileObservation(ctx, "partial", "", obs); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, err := st.ProfileSamples(ctx, "partial", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatalf("partial low evidence changed window: %v -> %v", before, unchanged)
	}
	for range 2 {
		if err := st.RecordProfileObservation(ctx, "partial", "", store.ProfileObservation{CPUMeasured: true, Partial: true, Duration: 2 * time.Minute, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 2000}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := st.GetPipelineProfile(ctx, "partial", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.PeakCores != 4 || p.SustainedCores != 2 || p.PeakMemoryBytes != 2000 || p.SampleCount != 18 || p.P99Duration != 2*time.Minute {
		t.Fatalf("raise-only profile = %+v", p)
	}
}

func TestPartialContendedProfileObservationNeverDecaysFloor(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	for _, obs := range []store.ProfileObservation{
		{CPUMeasured: true, Contended: true, PlanHash: "old", FloorCores: 4, FloorMemoryBytes: 1000},
		{CPUMeasured: true, Contended: true, Partial: true, FloorCores: 8, FloorMemoryBytes: 500},
		{CPUMeasured: true, Contended: true, Partial: true, FloorCores: 1, FloorMemoryBytes: 2000},
		{CPUMeasured: true, Contended: true, Partial: true, PlanHash: "new", FloorCores: 1, FloorMemoryBytes: 10},
	} {
		if err := st.RecordProfileObservation(ctx, "partial", "", obs); err != nil {
			t.Fatal(err)
		}
	}
	p, err := st.GetPipelineProfile(ctx, "partial", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanHash != "new" || p.FloorCores != 8 || p.FloorMemoryBytes != 2000 || p.SampleCount != 0 {
		t.Fatalf("partial contended floor = %+v", p)
	}
}

func TestPartialProfilePlanTransitionKeepsResourceBounds(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	for _, obs := range []store.ProfileObservation{
		{CPUMeasured: true, PlanHash: "old", Duration: time.Minute, PeakCores: 4, SustainedCores: 2, PeakMemoryBytes: 1000},
		{CPUMeasured: true, Partial: true, PlanHash: "new", Duration: 2 * time.Minute, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 2000},
	} {
		if err := st.RecordProfileObservation(ctx, "partial", "", obs); err != nil {
			t.Fatal(err)
		}
	}
	p, err := st.GetPipelineProfile(ctx, "partial", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanHash != "new" || p.PeakCores != 4 || p.SustainedCores != 2 || p.PeakMemoryBytes != 2000 || p.P99Duration != 2*time.Minute {
		t.Fatalf("partial plan transition profile = %+v", p)
	}
}

func TestPartialProfileObservationNeverGraduates(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	clean := store.ProfileObservation{CPUMeasured: true, Duration: time.Minute, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 1000}
	for range 2 {
		if err := st.RecordProfileObservation(ctx, "graduation", "", clean); err != nil {
			t.Fatal(err)
		}
	}
	raising := store.ProfileObservation{CPUMeasured: true, Partial: true, Duration: time.Minute, PeakCores: 2, SustainedCores: 1, PeakMemoryBytes: 4000}
	if err := st.RecordProfileObservation(ctx, "graduation", "", raising); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPipelineProfile(ctx, "graduation", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.PeakMemoryBytes != 4000 || p.SampleCount != 2 {
		t.Fatalf("profile = %+v, want the raised memory with only clean samples counted", p)
	}
}
