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
		pin        *Pin
		profile    *store.PipelineProfile
		numCPU     int
		wantCores  float64
		wantSource store.CostSource
		wantDur    time.Duration
	}{
		{
			name: "pin wins over measured", pin: &Pin{Cores: 2}, profile: measured, numCPU: 8,
			wantCores: 2, wantSource: store.CostSourcePin, wantDur: 30 * time.Second,
		},
		{
			name: "measured used when enough samples", pin: nil, profile: measured, numCPU: 8,
			wantCores: 6, wantSource: store.CostSourceMeasured, wantDur: 30 * time.Second,
		},
		{
			name: "below threshold falls to default", pin: nil, profile: thin, numCPU: 8,
			wantCores: 4, wantSource: store.CostSourceDefault,
		},
		{
			name: "no profile falls to default", pin: nil, profile: nil, numCPU: 16,
			wantCores: 8, wantSource: store.CostSourceDefault,
		},
		{
			name: "empty pin ignored", pin: &Pin{}, profile: measured, numCPU: 8,
			wantCores: 6, wantSource: store.CostSourceMeasured, wantDur: 30 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.pin, tc.profile, tc.numCPU, "")
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
	got := Resolve(nil, sleepHeavy, 8, "")
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
	got := Resolve(nil, blind, 8, "")
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
	if got := Resolve(nil, tiny, 8, ""); got.Cores != measuredCoreFloor {
		t.Errorf("Cores = %v, want the %v floor", got.Cores, measuredCoreFloor)
	}
}

func TestResolve_ColdStartSerializesOnBigMachine(t *testing.T) {
	got := Resolve(nil, nil, 32, "")
	if got.Cores != 16 {
		t.Errorf("cold-start cores = %v, want 16 (half of 32)", got.Cores)
	}
	if got.Cores*2 < 32 {
		t.Errorf("cold-start charge %v does not serialize two unknown runs", got.Cores)
	}
}

func TestResolve_ColdStartNeverBelowOne(t *testing.T) {
	if got := Resolve(nil, nil, 1, ""); got.Cores != 1 {
		t.Errorf("single-core machine cold-start = %v, want 1", got.Cores)
	}
}

func TestCheckDrift_Gating(t *testing.T) {
	measured := func(cores float64, samples int) *store.PipelineProfile {
		return &store.PipelineProfile{CPUMeasured: true, PeakCores: cores, SampleCount: samples}
	}

	cases := []struct {
		name      string
		pin       *Pin
		profile   *store.PipelineProfile
		wantNil   bool
		wantClass DriftClass
	}{
		{name: "unpinned never warns", pin: nil, profile: measured(9, 12), wantNil: true},
		{name: "empty pin never warns", pin: &Pin{}, profile: measured(9, 12), wantNil: true},
		{name: "too few samples stays quiet", pin: &Pin{Cores: 2}, profile: measured(9, 2), wantNil: true},
		{name: "within threshold stays quiet", pin: &Pin{Cores: 8}, profile: measured(9, 12), wantNil: true},
		{name: "under-pinned warns", pin: &Pin{Cores: 2}, profile: measured(9.1, 12), wantClass: driftUnderPinned},
		{name: "over-pinned warns", pin: &Pin{Cores: 16}, profile: measured(4, 12), wantClass: driftOverPinned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckDrift(tc.pin, tc.profile)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected no drift, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected a %s warning, got nil", tc.wantClass)
			}
			if got.Class != tc.wantClass {
				t.Errorf("Class = %q, want %q", got.Class, tc.wantClass)
			}
		})
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
	got := Resolve(nil, profile, 8, "")
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
	if got := Resolve(nil, profile, 8, ""); got.Cores != 6 {
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
	if got := Resolve(nil, tiny, 8, ""); got.Cores != measuredCoreFloor {
		t.Errorf("Cores = %v, want the %v floor", got.Cores, measuredCoreFloor)
	}
}

func TestCheckDrift_JudgesCorePinsAgainstTheChargedFigure(t *testing.T) {
	spiky := &store.PipelineProfile{
		CPUMeasured:    true,
		PeakCores:      8,
		SustainedCores: 2.6,
		SampleCount:    12,
	}
	if d := CheckDrift(&Pin{Cores: 3}, spiky); d != nil {
		t.Errorf("drift = %+v, want none: a pin of 3 agrees with the 2.6 it is charged", d)
	}
	d := CheckDrift(&Pin{Cores: 8}, spiky)
	if d == nil || d.Class != driftOverPinned {
		t.Fatalf("drift = %+v, want over-pinned: 8 reserves triple the 2.6 charge", d)
	}
	if d.MeasuredCores != 2.6 {
		t.Errorf("MeasuredCores = %v, want the charged 2.6", d.MeasuredCores)
	}
}

func TestCheckDrift_FallsBackToPeakBeforeSustainedExists(t *testing.T) {
	d := CheckDrift(&Pin{Cores: 2}, &store.PipelineProfile{CPUMeasured: true, PeakCores: 8, SampleCount: 12})
	if d == nil || d.MeasuredCores != 8 {
		t.Fatalf("drift = %+v, want under-pinned against the 8.0 peak", d)
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
	got := Resolve(nil, changed, 8, "new")
	if got.Cores != 2 {
		t.Errorf("Cores = %v, want the predecessor's 2.0 charge, not its 8.0 peak", got.Cores)
	}
	if got.Source != store.CostSourceMeasuring {
		t.Errorf("Source = %q, want measuring", got.Source)
	}
}

func TestResolve_WarmStartFallsBackToPrevPeak(t *testing.T) {
	changed := &store.PipelineProfile{CPUMeasured: true, PlanHash: "old", PrevPeakCores: 8, SampleCount: 1}
	if got := Resolve(nil, changed, 8, "new"); got.Cores != 8 {
		t.Errorf("Cores = %v, want the carried 8.0 peak", got.Cores)
	}
}

func TestCheckDrift_MessageCarriesExactFix(t *testing.T) {
	d := CheckDrift(&Pin{Cores: 2}, &store.PipelineProfile{CPUMeasured: true, PeakCores: 9.1, SampleCount: 12})
	if d == nil {
		t.Fatal("expected drift")
	}
	want := "resource pin: 2 cores; measured sustained p95 9.1 cores over 12 runs - update or remove the pin"
	if d.Message != want {
		t.Errorf("Message = %q, want %q", d.Message, want)
	}
}
