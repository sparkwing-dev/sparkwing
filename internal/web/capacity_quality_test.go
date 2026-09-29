package web

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestChargeChainRejectsUnknownResourceHistory(t *testing.T) {
	profile := store.PipelineProfile{SampleCount: 3, PeakCores: 8, PeakMemoryBytes: 100, PrevPeakCores: 12, PrevPeakMemoryBytes: 200, FloorCores: 20, FloorMemoryBytes: 300}
	seen := map[string]int{}
	for _, step := range chargeChain(profile, 8) {
		if step.Step == "measured" || step.Step == "prev_charge" || step.Step == "floor" {
			seen[step.Step]++
			if step.Eligible || step.Applied {
				t.Errorf("unknown history shown as eligible: %+v", step)
			}
		}
	}
	for _, name := range []string{"measured", "prev_charge", "floor"} {
		if seen[name] != 1 {
			t.Errorf("step %s appears %d times, want once", name, seen[name])
		}
	}
}
