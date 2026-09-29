package capacity

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestResolvePeak(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pin        *Pin
		profile    *store.PipelineProfile
		hash       string
		peak, host float64
		memory     int64
		source     store.CostSource
	}{
		{name: "cold start", peak: 4, host: 4, source: store.CostSourceDefault},
		{name: "pin", pin: &Pin{Cores: 3, MemoryBytes: 1024}, peak: 3, host: 3, memory: 1024, source: store.CostSourcePin},
		{name: "measured", profile: &store.PipelineProfile{PlanHash: "a", SampleCount: 3, CPUMeasured: true, PeakCores: 2, SustainedCores: 0.5, PeakMemoryBytes: 512}, hash: "a", peak: 2, host: 0.5, memory: 512, source: store.CostSourceMeasured},
		{name: "changed plan", profile: &store.PipelineProfile{PlanHash: "a", SampleCount: 3, CPUMeasured: true, PeakCores: 2, SustainedCores: 0.5, PeakMemoryBytes: 512}, hash: "b", peak: 2, host: 0.5, memory: 512, source: store.CostSourceMeasuring},
		{name: "previous plan", profile: &store.PipelineProfile{PrevPeakCores: 2, PrevSustainedCores: 0.5, PrevPeakMemoryBytes: 512}, peak: 2, host: 0.5, memory: 512, source: store.CostSourceMeasuring},
		{name: "changed empty plan", profile: &store.PipelineProfile{PlanHash: "b", PrevPeakCores: 2, PrevSustainedCores: 0.5, PrevPeakMemoryBytes: 512}, hash: "c", peak: 2, host: 0.5, memory: 512, source: store.CostSourceMeasuring},
		{name: "floor", profile: &store.PipelineProfile{PrevPeakCores: 2, PrevSustainedCores: 0.5, FloorCores: 3, FloorMemoryBytes: 512}, peak: 6, host: 6, memory: 1024, source: store.CostSourceFloor},
		{name: "measured zero", profile: &store.PipelineProfile{SampleCount: 3, CPUMeasured: true}, peak: 0.1, host: 0.1, source: store.CostSourceMeasured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var duration time.Duration
			if tc.profile != nil {
				tc.profile.P50Duration = time.Minute
				duration = time.Minute
			}
			want := Resolution{Cores: tc.peak, MemoryBytes: tc.memory, Source: tc.source, ExpectedDuration: duration}
			if got := ResolvePeak(tc.pin, tc.profile, 8, tc.hash); got != want {
				t.Errorf("pod allocation = %+v, want %+v", got, want)
			}
			want.Cores = tc.host
			if got := Resolve(tc.pin, tc.profile, 8, tc.hash); got != want {
				t.Errorf("host admission = %+v, want %+v", got, want)
			}
		})
	}
}
