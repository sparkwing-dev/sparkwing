package capacity

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestResolveRequiresMeasuredResourceHistory(t *testing.T) {
	for _, hash := range []string{"a", "b"} {
		p := &store.PipelineProfile{PlanHash: "a", SampleCount: 3, PeakCores: 8, SustainedCores: 6, PeakMemoryBytes: 100, PrevPeakCores: 12, PrevSustainedCores: 10, PrevPeakMemoryBytes: 200}
		got := Resolve(p, 8, hash)
		if got.Cores != 4 || got.MemoryBytes != 0 || got.Source != store.CostSourceDefault {
			t.Errorf("unknown resource history influenced charge: %+v", got)
		}
	}
	for _, cpu := range []float64{0, 2} {
		p := &store.PipelineProfile{CPUMeasured: true, SampleCount: 3, PeakCores: cpu, PeakMemoryBytes: 100}
		got := Resolve(p, 8, "")
		if got.Source != store.CostSourceMeasured || got.Cores != max(cpu, 0.1) || got.MemoryBytes != 100 {
			t.Errorf("measured control changed: %+v", got)
		}
	}
}

func TestResolveRetainsMeasuredCarry(t *testing.T) {
	p := &store.PipelineProfile{CPUMeasured: true, PrevPeakCores: 3, PrevSustainedCores: 3, PrevPeakMemoryBytes: 100}
	got := Resolve(p, 8, "")
	if got.Cores != 3 || got.MemoryBytes != 100 || got.Source != store.CostSourceMeasuring {
		t.Errorf("measured carry lost: %+v", got)
	}
}
