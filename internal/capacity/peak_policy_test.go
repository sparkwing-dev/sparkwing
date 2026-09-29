package capacity

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCPUResourcePoliciesPreservePinsAndVersionTransitions(t *testing.T) {
	for _, tc := range []struct {
		name              string
		pin               *Pin
		planHash          string
		wantSource        store.CostSource
		wantHost, wantPod float64
		wantMemory        int64
	}{
		{"pin", &Pin{Cores: 7, MemoryBytes: 8192}, "new", store.CostSourcePin, 7, 7, 8192},
		{"same version", nil, "old", store.CostSourceMeasured, 0.5, 2, 4096},
		{"changed version", nil, "new", store.CostSourceMeasuring, 0.5, 2, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &store.PipelineProfile{
				PlanHash: "old", SampleCount: 3, CPUMeasured: true,
				PeakCores: 2, SustainedCores: new(float64(0.5)), PeakMemoryBytes: 4096,
				PrevPeakCores: 9, PrevSustainedCores: new(float64(3)), PrevPeakMemoryBytes: 2048,
				P50Duration: time.Second,
			}
			for name, got := range map[string]Resolution{
				"host": Resolve(tc.pin, profile, 8, tc.planHash),
				"pod":  ResolvePeak(tc.pin, profile, 8, tc.planHash),
			} {
				wantCPU := tc.wantHost
				if name == "pod" {
					wantCPU = tc.wantPod
				}
				if got.Cores != wantCPU || got.MemoryBytes != tc.wantMemory || got.Source != tc.wantSource || got.ExpectedDuration != time.Second {
					t.Errorf("%s: got %+v; want %v cores, %d bytes, source %s and one second", name, got, wantCPU, tc.wantMemory, tc.wantSource)
				}
			}
		})
	}
}

func TestPeakDriftUsesPeakWithoutInventingSustainedCPU(t *testing.T) {
	profile := &store.PipelineProfile{PeakCores: 2, CPUMeasured: true, SampleCount: 3}
	pin := &Pin{Cores: 4}
	if got := CheckDrift(pin, profile); got != nil {
		t.Fatalf("host drift invented sustained CPU: %+v", got)
	}
	got := CheckPeakDrift(pin, profile)
	if got == nil || got.Class != DriftOverPinned || got.MeasuredCores != 2 || !strings.Contains(got.Message, "measured peak p95 2 cores") {
		t.Fatalf("peak drift = %+v; want over-pinned against peak p95 2 cores", got)
	}
	if profile.SustainedCores != nil {
		t.Fatal("peak drift changed sustained availability")
	}
}
