package store_test

import (
	"context"
	"encoding/json"
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
	st := storetest.Open(t)
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

func TestProfileObservation_DistinguishesOmittedAndZeroSustainedCPU(t *testing.T) {
	var omitted, measuredZero store.ProfileObservation
	if err := json.Unmarshal([]byte(`{"PeakCores":6,"CPUMeasured":true}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"PeakCores":6,"SustainedCores":0,"CPUMeasured":true}`), &measuredZero); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(omitted, measuredZero) {
		t.Fatal("profile input loses whether sustained CPU was omitted or measured as zero")
	}
}

func TestPipelineProfile_PreservesSustainedPresence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sustained *float64
	}{
		{"omitted", nil}, {"measured zero", new(float64(0))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			for range 3 {
				if err := st.RecordProfileObservation(t.Context(), "parcels", "sort", store.ProfileObservation{
					Duration: time.Second, PeakCores: 6, SustainedCores: tc.sustained,
					PeakMemoryBytes: 4096, CPUMeasured: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			profile, err := st.GetPipelineProfile(t.Context(), "parcels", "sort")
			if err != nil {
				t.Fatal(err)
			}
			if profile == nil {
				t.Fatal("missing profile")
			}
			if !reflect.DeepEqual(profile.SustainedCores, tc.sustained) || profile.PeakCores != 6 {
				t.Fatalf("sustained presence changed: got=%v want=%v peak=%v", profile.SustainedCores, tc.sustained, profile.PeakCores)
			}
		})
	}
}
