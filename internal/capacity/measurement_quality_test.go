package capacity

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResolveRequiresMeasuredResourceHistory(t *testing.T) {
	for _, hash := range []string{"a", "b"} {
		p := &store.PipelineProfile{PlanHash: "a", SampleCount: 3, PeakCores: 8, SustainedCores: 6, PeakMemoryBytes: 100, PrevPeakCores: 12, PrevSustainedCores: 10, PrevPeakMemoryBytes: 200, FloorCores: 20, FloorMemoryBytes: 300}
		for _, resolve := range []func(*Pin, *store.PipelineProfile, int, string) Resolution{Resolve, ResolvePeak} {
			got := resolve(nil, p, 8, hash)
			if got.Cores != 4 || got.MemoryBytes != 0 || got.Source != store.CostSourceDefault {
				t.Errorf("unknown resource history influenced charge: %+v", got)
			}
			got = resolve(&Pin{Cores: 3, MemoryBytes: 50}, p, 8, hash)
			if got.Cores != 3 || got.MemoryBytes != 50 || got.Source != store.CostSourcePin {
				t.Errorf("explicit pin changed: %+v", got)
			}
		}
		p.SampleCount = 0
		if FloorPoisoned(p, 4) {
			t.Error("unknown floor reported poisoned")
		}
	}
	for _, cpu := range []float64{0, 2} {
		p := &store.PipelineProfile{CPUMeasured: true, SampleCount: 3, PeakCores: cpu, PeakMemoryBytes: 100}
		got := Resolve(nil, p, 8, "")
		if got.Source != store.CostSourceMeasured || got.Cores != max(cpu, 0.1) || got.MemoryBytes != 100 {
			t.Errorf("measured control changed: %+v", got)
		}
	}
}

func TestResolveRetainsMeasuredCarryAndFloor(t *testing.T) {
	for _, resolve := range []func(*Pin, *store.PipelineProfile, int, string) Resolution{Resolve, ResolvePeak} {
		p := &store.PipelineProfile{CPUMeasured: true, PrevPeakCores: 3, PrevSustainedCores: 3, PrevPeakMemoryBytes: 100}
		got := resolve(nil, p, 8, "")
		if got.Cores != 3 || got.MemoryBytes != 100 || got.Source != store.CostSourceMeasuring {
			t.Errorf("measured carry lost: %+v", got)
		}
		p.FloorCores, p.FloorMemoryBytes = 2, 100
		got = resolve(nil, p, 8, "")
		if got.Cores != 4 || got.MemoryBytes != 200 || got.Source != store.CostSourceFloor {
			t.Errorf("measured floor lost: %+v", got)
		}
	}
}

func TestCheckDriftRequiresMeasuredResourceHistory(t *testing.T) {
	for _, pin := range []*Pin{{Cores: 1}, {MemoryBytes: 100}} {
		p := &store.PipelineProfile{SampleCount: 3, PeakCores: 8, PeakMemoryBytes: 800}
		if got := CheckDrift(pin, p); got != nil {
			t.Errorf("unknown resource history produced drift: %+v", got)
		}
		p.CPUMeasured = true
		if got := CheckDrift(pin, p); got == nil {
			t.Error("measured drift was suppressed")
		}
	}
}
