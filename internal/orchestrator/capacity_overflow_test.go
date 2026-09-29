package orchestrator

import (
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfileRejectsOverflow(t *testing.T) {
	for _, test := range []struct {
		name        string
		cpu, memory int64
		nodes       int
		wantMemory  int64
	}{
		{"negative memory wrap", 500, math.MaxInt64, 2, 0},
		{"negative CPU wrap", math.MaxInt64, 4096, 2, 0},
		{"positive memory wrap", 500, math.MaxInt64, 3, 0},
		{"positive CPU wrap", math.MaxInt64, 4096, 3, 0},
		{"representable memory sum", 250, 3074457345618258602, 3, 9223372036854775806},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, start := seedUsageRun(t, "overflow", []usageNode{{id: "a", dur: 2 * time.Second, wall: 2 * time.Second}, {id: "b", dur: 2 * time.Second, wall: 2 * time.Second}, {id: "c", dur: 2 * time.Second, wall: 2 * time.Second}}[:test.nodes])
			for _, id := range []string{"a", "b", "c"}[:test.nodes] {
				if err := st.AddNodeMetricSample(t.Context(), "r1", id, store.MetricSample{TS: start, Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: test.cpu, MemoryBytes: test.memory}); err != nil {
					t.Fatal(err)
				}
			}
			recordRunProfile(t.Context(), localState{st: st}, "overflow", "r1", nil, "", runCharge{}, false, start, start.Add(2*time.Second))
			profile, err := st.GetPipelineProfile(t.Context(), "overflow", "")
			if err != nil {
				t.Fatal(err)
			}
			if test.wantMemory == 0 {
				if profile != nil {
					t.Fatalf("unrepresentable sum became a run profile: %+v", profile)
				}
			} else if profile == nil || profile.SampleCount != 1 || profile.PeakMemoryBytes != test.wantMemory || profile.PeakCores != 0.75 {
				t.Fatalf("representable sum changed: %+v", profile)
			}
			for _, id := range []string{"a", "b", "c"}[:test.nodes] {
				profile, err := st.GetPipelineProfile(t.Context(), "overflow", id)
				if err != nil {
					t.Fatal(err)
				}
				if profile == nil || profile.SampleCount != 1 || profile.PeakMemoryBytes != test.memory {
					t.Fatalf("valid node %s lost its profile: %+v", id, profile)
				}
			}
		})
	}
}
