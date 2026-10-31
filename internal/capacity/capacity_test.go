package capacity

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestResolve_Order(t *testing.T) {
	measured := &store.PipelineProfile{
		CPUMeasured:     true,
		P50Duration:     30 * time.Second,
		PeakCores:       6,
		PeakMemoryBytes: 4 << 30,
		SampleCount:     MinSamples,
	}
	thin := &store.PipelineProfile{CPUMeasured: true, PeakCores: 6, SampleCount: MinSamples - 1}

	cases := []struct {
		name       string
		profile    *store.PipelineProfile
		numCPU     int
		wantCores  float64
		wantSource store.CostSource
		wantDur    time.Duration
	}{
		{
			name: "measured used when enough samples", profile: measured, numCPU: 8,
			wantCores: 6, wantSource: store.CostSourceMeasured, wantDur: 30 * time.Second,
		},
		{
			name: "below threshold falls to default", profile: thin, numCPU: 8,
			wantCores: 4, wantSource: store.CostSourceDefault,
		},
		{
			name: "no profile falls to default", profile: nil, numCPU: 16,
			wantCores: 8, wantSource: store.CostSourceDefault,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.profile, tc.numCPU, "")
			if got.Cores != tc.wantCores {
				t.Errorf("Cores = %v, want %v", got.Cores, tc.wantCores)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.ExpectedDuration != tc.wantDur {
				t.Errorf("ExpectedDuration = %s, want %s", got.ExpectedDuration, tc.wantDur)
			}
		})
	}
}

func TestResolve_ZeroCPUProfileQualifiesOnHealthySampler(t *testing.T) {
	sleepHeavy := &store.PipelineProfile{
		P50Duration:     10 * time.Second,
		PeakCores:       0,
		PeakMemoryBytes: 256 << 20,
		SampleCount:     MinSamples,
		CPUMeasured:     true,
	}
	got := Resolve(sleepHeavy, 8, "")
	if got.Source != store.CostSourceMeasured {
		t.Fatalf("Source = %q, want measured (healthy sampler, near-zero peak)", got.Source)
	}
	if got.Cores != measuredCoreFloor {
		t.Errorf("Cores = %v, want the %v core floor", got.Cores, measuredCoreFloor)
	}
	if got.MemoryBytes != 256<<20 {
		t.Errorf("MemoryBytes = %d, want the measured 256MiB", got.MemoryBytes)
	}
}

func TestResolve_ZeroCPUProfileStaysConservativeOnBlindSampler(t *testing.T) {
	blind := &store.PipelineProfile{
		P50Duration:     10 * time.Second,
		PeakCores:       0,
		PeakMemoryBytes: 256 << 20,
		SampleCount:     MinSamples,
		CPUMeasured:     false,
	}
	got := Resolve(blind, 8, "")
	if got.Source != store.CostSourceDefault {
		t.Fatalf("Source = %q, want default (blind sampler's zero is not a measurement)", got.Source)
	}
	if got.Cores != coldStartCores(8) {
		t.Errorf("Cores = %v, want the cold-start default %v", got.Cores, coldStartCores(8))
	}
}

func TestResolve_MeasuredPeakBelowFloorLiftsToFloor(t *testing.T) {
	tiny := &store.PipelineProfile{
		PeakCores:   0.05,
		SampleCount: MinSamples,
		CPUMeasured: true,
	}
	if got := Resolve(tiny, 8, ""); got.Cores != measuredCoreFloor {
		t.Errorf("Cores = %v, want the %v floor", got.Cores, measuredCoreFloor)
	}
}

func TestResolve_ColdStartSerializesOnBigMachine(t *testing.T) {
	got := Resolve(nil, 32, "")
	if got.Cores != 16 {
		t.Errorf("cold-start cores = %v, want 16 (half of 32)", got.Cores)
	}
	if got.Cores*2 < 32 {
		t.Errorf("cold-start charge %v does not serialize two unknown runs", got.Cores)
	}
}

func TestResolve_ColdStartNeverBelowOne(t *testing.T) {
	if got := Resolve(nil, 1, ""); got.Cores != 1 {
		t.Errorf("single-core machine cold-start = %v, want 1", got.Cores)
	}
}

func TestResolve_MeasuredChargesSustainedCores(t *testing.T) {
	profile := &store.PipelineProfile{
		PeakCores:       6,
		SustainedCores:  1.5,
		PeakMemoryBytes: 4 << 30,
		SampleCount:     MinSamples,
		CPUMeasured:     true,
	}
	got := Resolve(profile, 8, "")
	if got.Cores != 1.5 {
		t.Errorf("Cores = %v, want the sustained 1.5, not the 6.0 peak", got.Cores)
	}
	if got.MemoryBytes != 4<<30 {
		t.Errorf("MemoryBytes = %d, want the peak %d", got.MemoryBytes, int64(4)<<30)
	}
	if got.Source != store.CostSourceMeasured {
		t.Errorf("Source = %q, want measured", got.Source)
	}
}

func TestResolve_ProfileWithoutSustainedFallsBackToPeak(t *testing.T) {
	profile := &store.PipelineProfile{
		PeakCores:       6,
		PeakMemoryBytes: 4 << 30,
		SampleCount:     MinSamples,
		CPUMeasured:     true,
	}
	if got := Resolve(profile, 8, ""); got.Cores != 6 {
		t.Errorf("Cores = %v, want the 6.0 peak while no sustained figure exists", got.Cores)
	}
}

func TestResolve_SustainedBelowFloorLiftsToFloor(t *testing.T) {
	tiny := &store.PipelineProfile{
		PeakCores:      6,
		SustainedCores: 0.02,
		SampleCount:    MinSamples,
		CPUMeasured:    true,
	}
	if got := Resolve(tiny, 8, ""); got.Cores != measuredCoreFloor {
		t.Errorf("Cores = %v, want the %v floor", got.Cores, measuredCoreFloor)
	}
}

func TestResolve_WarmStartAfterPlanHashChangePricesAtParity(t *testing.T) {
	changed := &store.PipelineProfile{
		CPUMeasured:        true,
		PlanHash:           "old",
		PrevPeakCores:      8,
		PrevSustainedCores: 2,
		SampleCount:        1,
	}
	got := Resolve(changed, 8, "new")
	if got.Cores != 2 {
		t.Errorf("Cores = %v, want the predecessor's 2.0 charge, not its 8.0 peak", got.Cores)
	}
	if got.Source != store.CostSourceMeasuring {
		t.Errorf("Source = %q, want measuring", got.Source)
	}
}

func TestResolve_WarmStartFallsBackToPrevPeak(t *testing.T) {
	changed := &store.PipelineProfile{CPUMeasured: true, PlanHash: "old", PrevPeakCores: 8, SampleCount: 1}
	if got := Resolve(changed, 8, "new"); got.Cores != 8 {
		t.Errorf("Cores = %v, want the carried 8.0 peak", got.Cores)
	}
}
