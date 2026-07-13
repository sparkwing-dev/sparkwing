package capacity

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestApplyHostCeiling(t *testing.T) {
	cases := []struct {
		name        string
		res         Resolution
		machine     float64
		grantCores  float64
		grantMem    int64
		wantCores   float64
		wantMem     int64
		wantWarning string
	}{
		{
			name:        "pin over capacity clamps with loud warning",
			res:         Resolution{Cores: 16, Source: store.CostSourcePin},
			machine:     10,
			grantCores:  8,
			wantCores:   8,
			wantWarning: "pin 16.0 cores exceeds this machine (10.0); running alone - consider a smaller pin or a machine budget",
		},
		{
			name:       "measured over capacity clamps silently",
			res:        Resolution{Cores: 18.9, Source: store.CostSourceMeasured},
			machine:    14,
			grantCores: 11.2,
			wantCores:  11.2,
		},
		{
			name:       "within ceiling untouched",
			res:        Resolution{Cores: 4, Source: store.CostSourcePin},
			machine:    10,
			grantCores: 8,
			wantCores:  4,
		},
		{
			name:       "unknown ceiling leaves charge",
			res:        Resolution{Cores: 16, Source: store.CostSourcePin},
			grantCores: 0,
			wantCores:  16,
		},
		{
			name:       "memory clamps to grantable",
			res:        Resolution{Cores: 1, MemoryBytes: 32 << 30, Source: store.CostSourceMeasured},
			grantCores: 8,
			grantMem:   16 << 30,
			wantCores:  1,
			wantMem:    16 << 30,
		},
		{
			name:        "memory pin over capacity clamps with loud warning",
			res:         Resolution{Cores: 1, MemoryBytes: 32 << 30, Source: store.CostSourcePin},
			grantCores:  8,
			grantMem:    16 << 30,
			wantCores:   1,
			wantMem:     16 << 30,
			wantWarning: "pin 32GB memory exceeds this machine (16GB); running alone - consider a smaller pin or a machine budget",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warning := ApplyHostCeiling(tc.res, tc.machine, tc.grantCores, tc.grantMem)
			if got.Cores != tc.wantCores {
				t.Errorf("cores = %v, want %v", got.Cores, tc.wantCores)
			}
			if got.MemoryBytes != tc.wantMem {
				t.Errorf("memory = %d, want %d", got.MemoryBytes, tc.wantMem)
			}
			if warning != tc.wantWarning {
				t.Errorf("warning = %q, want %q", warning, tc.wantWarning)
			}
		})
	}
}

func TestResolve_Order(t *testing.T) {
	measured := &store.PipelineProfile{
		P50Duration:     30 * time.Second,
		P99Duration:     90 * time.Second,
		PeakCores:       6,
		PeakMemoryBytes: 4 << 30,
		SampleCount:     MinSamples,
	}
	thin := &store.PipelineProfile{PeakCores: 6, SampleCount: MinSamples - 1}

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
			wantCores: 2, wantSource: store.CostSourcePin, wantDur: 90 * time.Second,
		},
		{
			name: "measured used when enough samples", pin: nil, profile: measured, numCPU: 8,
			wantCores: 6, wantSource: store.CostSourceMeasured, wantDur: 90 * time.Second,
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
			wantCores: 6, wantSource: store.CostSourceMeasured, wantDur: 90 * time.Second,
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
		return &store.PipelineProfile{PeakCores: cores, SampleCount: samples}
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
		{name: "under-pinned warns", pin: &Pin{Cores: 2}, profile: measured(9.1, 12), wantClass: DriftUnderPinned},
		{name: "over-pinned warns", pin: &Pin{Cores: 16}, profile: measured(4, 12), wantClass: DriftOverPinned},
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

func TestCheckDrift_MessageCarriesExactFix(t *testing.T) {
	d := CheckDrift(&Pin{Cores: 2}, &store.PipelineProfile{PeakCores: 9.1, SampleCount: 12})
	if d == nil {
		t.Fatal("expected drift")
	}
	want := "resource pin: 2 cores; measured p99 9.1 cores over 12 runs - update or remove the pin"
	if d.Message != want {
		t.Errorf("Message = %q, want %q", d.Message, want)
	}
}
