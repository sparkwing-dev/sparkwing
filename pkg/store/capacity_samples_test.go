package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestProfileSamples_ReturnsWindowOldestFirst(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	for _, obs := range []store.ProfileObservation{
		{Duration: 10 * time.Second, PeakCores: 2.0, SustainedCores: 1.0, PeakMemoryBytes: 1 << 30},
		{Duration: 20 * time.Second, PeakCores: 4.0, SustainedCores: 3.0, PeakMemoryBytes: 2 << 30},
		{Duration: 30 * time.Second, PeakCores: 8.0, SustainedCores: 5.0, PeakMemoryBytes: 3 << 30},
	} {
		if err := st.RecordProfileObservation(ctx, "demo", "", obs); err != nil {
			t.Fatalf("RecordProfileObservation: %v", err)
		}
	}

	samples, err := st.ProfileSamples(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	if samples[0].Duration != 10*time.Second || samples[2].Duration != 30*time.Second {
		t.Fatalf("window is not oldest-first: %+v", samples)
	}
	if samples[2].SustainedCores != 5.0 || samples[2].PeakCores != 8.0 {
		t.Errorf("newest sample = %+v, want peak 8 sustained 5", samples[2])
	}
	if samples[1].PeakMemoryBytes != 2<<30 {
		t.Errorf("PeakMemoryBytes = %d, want %d", samples[1].PeakMemoryBytes, 2<<30)
	}
}

func TestProfileSamples_ReproduceStoredCharges(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()

	for i := 1; i <= 12; i++ {
		if err := st.RecordProfileObservation(ctx, "demo", "", store.ProfileObservation{
			Duration:        time.Duration(i) * time.Second,
			PeakCores:       float64(i),
			SustainedCores:  float64(i) / 2,
			PeakMemoryBytes: int64(i) << 20,
		}); err != nil {
			t.Fatal(err)
		}
	}

	prof, err := st.GetPipelineProfile(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	samples, err := st.ProfileSamples(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}

	sustained := make([]float64, len(samples))
	mems := make([]float64, len(samples))
	for i, s := range samples {
		sustained[i] = s.SustainedCores
		mems[i] = float64(s.PeakMemoryBytes)
	}
	if got := store.NearestRankPercentile(sustained, 0.95); got != prof.SustainedCores {
		t.Errorf("recomputed sustained p95 = %v, stored SustainedCores = %v", got, prof.SustainedCores)
	}
	if got := int64(store.NearestRankPercentile(mems, 0.95)); got != prof.PeakMemoryBytes {
		t.Errorf("recomputed memory p95 = %d, stored PeakMemoryBytes = %d", got, prof.PeakMemoryBytes)
	}

	idx := store.NearestRankIndex(sustained, 0.95)
	if idx < 0 || idx >= len(samples) {
		t.Fatalf("NearestRankIndex = %d, out of range for %d samples", idx, len(samples))
	}
	if samples[idx].SustainedCores != prof.SustainedCores {
		t.Errorf("selected sample %d has sustained %v, charge is %v",
			idx, samples[idx].SustainedCores, prof.SustainedCores)
	}
}

func TestProfileSamples_AbsentProfileReturnsNil(t *testing.T) {
	samples, err := storetest.Open(t).ProfileSamples(context.Background(), "never-run", "")
	if err != nil {
		t.Fatal(err)
	}
	if samples != nil {
		t.Fatalf("samples = %+v, want nil for an unmeasured pipeline", samples)
	}
}

func TestNearestRankIndex_TiesPickTheOldestSample(t *testing.T) {
	xs := []float64{4, 4, 4, 4}
	if got := store.NearestRankIndex(xs, 0.95); got != 3 {
		t.Errorf("NearestRankIndex on a flat window = %d, want the rank position 3", got)
	}
	if got := store.NearestRankIndex(nil, 0.95); got != -1 {
		t.Errorf("NearestRankIndex(nil) = %d, want -1", got)
	}

	vals := []float64{9, 1, 5, 7, 3}
	idx := store.NearestRankIndex(vals, 0.5)
	if vals[idx] != store.NearestRankPercentile(vals, 0.5) {
		t.Errorf("index %d holds %v, percentile is %v", idx, vals[idx], store.NearestRankPercentile(vals, 0.5))
	}
}
