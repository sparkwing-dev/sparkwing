package web

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCapacityChainUsesMeasuredZero(t *testing.T) {
	for _, tc := range []struct {
		name, wantStep string
		profile        store.PipelineProfile
		wantFloor      bool
	}{
		{"current", "measured", store.PipelineProfile{SampleCount: 3, CPUMeasured: true, SustainedCores: new(float64(0))}, true},
		{"previous", "prev_charge", store.PipelineProfile{PrevSustainedCores: new(float64(0))}, true},
		{"absent sustained", "cold_start", store.PipelineProfile{SampleCount: 3, CPUMeasured: true, PeakCores: 2}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			floor, source := false, false
			for _, step := range chargeChain(tc.profile, 8) {
				if step.Applied && !step.Eligible {
					t.Errorf("measured-zero chain applies an ineligible step: %+v", step)
				}
				if step.Step == tc.wantStep {
					source = true
					if !step.Eligible || !step.Applied {
						t.Errorf("measured-zero chain source = %+v; want eligible and applied", step)
					}
				}
				if step.Step == "measured_floor" {
					floor = true
					if !step.Applied || step.Cores != 0.1 {
						t.Errorf("measured-zero chain floor = %+v; want applied 0.1 cores", step)
					}
				}
			}
			if !source {
				t.Errorf("measured-zero chain omits source step %s", tc.wantStep)
			}
			if floor != tc.wantFloor {
				t.Errorf("measured-zero chain floor presence = %t; want %t", floor, tc.wantFloor)
			}
		})
	}
}
