package store_test

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPipelineProfileMemoryFloorIntegers(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		initial, observed, want int64
	}{
		{"exact observation", 0, 9007199254740993, 9007199254740993},
		{"larger observation", 9007199254740993, 18014398509481987, 18014398509481987},
		{"exact decay", 18014398509481987, 1, 9007199254740993},
		{"maximum observation", 0, math.MaxInt64, math.MaxInt64},
		{"maximum decay", math.MaxInt64, 1, math.MaxInt64 / 2},
		{"observed above half", 10, 8, 8},
		{"zero observation", 10, 0, 5},
		{"odd decay", 5, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := storetest.Open(t)
			if err := st.RecordProfileObservation(t.Context(), "memory", "node", store.ProfileObservation{CPUMeasured: true, Contended: true, FloorMemoryBytes: tc.initial}); err != nil {
				t.Fatal(err)
			}
			if err := st.RecordProfileObservation(t.Context(), "memory", "node", store.ProfileObservation{CPUMeasured: true, Contended: true, FloorMemoryBytes: tc.observed}); err != nil {
				t.Fatal(err)
			}
			profile, err := st.GetPipelineProfile(t.Context(), "memory", "node")
			if err != nil || profile == nil {
				t.Fatalf("profile = %+v, error = %v", profile, err)
			}
			if profile.FloorMemoryBytes != tc.want {
				t.Errorf("floor = %d, want %d", profile.FloorMemoryBytes, tc.want)
			}
		})
	}
}
