package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPipelineProfile_PeakCarryIsIndependentOfSustained(t *testing.T) {
	for _, priorSustained := range []*float64{nil, new(float64(0)), new(float64(3))} {
		name := "peak only"
		if priorSustained != nil {
			name = fmt.Sprintf("sustained %g", *priorSustained)
		}
		for _, peak := range []float64{0, 2} {
			name := fmt.Sprintf("%s/peak %g", name, peak)
			memory := int64(peak * 1000)
			t.Run(name, func(t *testing.T) {
				st := storetest.Open(t)
				observations := []store.ProfileObservation{
					{PlanHash: "A", Duration: time.Second, PeakCores: 9, SustainedCores: priorSustained, PeakMemoryBytes: 9000, CPUMeasured: true},
					{PlanHash: "B", Duration: time.Second, PeakCores: peak, PeakMemoryBytes: memory, CPUMeasured: true},
					{PlanHash: "C", Contended: true},
					{PlanHash: "D", Contended: true},
				}
				for i, obs := range observations {
					if err := st.RecordProfileObservation(t.Context(), "parcels", "sort", obs); err != nil {
						t.Fatal(err)
					}
					if i < 2 {
						continue
					}
					got, err := st.GetPipelineProfile(t.Context(), "parcels", "sort")
					if err != nil || got == nil {
						t.Fatalf("profile=%+v error=%v", got, err)
					}
					if got.PrevPeakCores != peak || got.PrevPeakMemoryBytes != memory {
						t.Errorf("after %s: previous peak = %v cores / %d bytes; want B's %g cores / %d bytes", obs.PlanHash, got.PrevPeakCores, got.PrevPeakMemoryBytes, peak, memory)
					}
					if priorSustained == nil && got.PrevSustainedCores != nil || priorSustained != nil && (got.PrevSustainedCores == nil || *got.PrevSustainedCores != *priorSustained) {
						t.Errorf("after %s: sustained carry = %v; want unchanged prior sustained availability", obs.PlanHash, got.PrevSustainedCores)
					}
				}
			})
		}
	}
}
