package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestPipelineProfile_InvalidHistoryCannotRetainFloorsOrCarry(t *testing.T) {
	for _, schema := range []int{3, 4} {
		for _, count := range []int{0, 3} {
			for _, contended := range []bool{false, true} {
				t.Run(fmt.Sprintf("schema%d/count%d/contended%t", schema, count, contended), func(t *testing.T) {
					st := storetest.Open(t)
					if err := st.UpsertProfilePin(t.Context(), "parcels", "sort", 7, 8192); err != nil {
						t.Fatal(err)
					}
					_, err := st.DB().ExecContext(t.Context(), fmt.Sprintf(`UPDATE pipeline_profiles SET
 sample_count=%d, samples_json='{"schema":%d,"samples":[{"d":1000000000,"c":60,"s":60,"m":9000}]}',
 peak_cores=60, sustained_cores=60, peak_memory_bytes=9000, cpu_measured=1,
 floor_cores=40, floor_memory_bytes=8000, prev_peak_cores=50, prev_sustained_cores=50,
 prev_peak_memory_bytes=7000, plan_hash='old'
 WHERE pipeline='parcels' AND node_id='sort'`, count, schema))
					if err != nil {
						t.Fatal(err)
					}
					check := func(wantCount int, wantFloor float64) *store.PipelineProfile {
						t.Helper()
						p, err := st.GetPipelineProfile(t.Context(), "parcels", "sort")
						if err != nil || p == nil {
							t.Fatalf("profile=%+v error=%v", p, err)
						}
						if p.PinnedCores != 7 || p.PinnedMemoryBytes != 8192 {
							t.Fatalf("pins changed: %+v", p)
						}
						if p.SampleCount != wantCount || p.FloorCores != wantFloor || p.FloorMemoryBytes != 0 || p.PrevPeakCores != 0 || p.PrevSustainedCores != nil || p.PrevPeakMemoryBytes != 0 {
							t.Fatalf("stale evidence retained: %+v", p)
						}
						return p
					}
					before := check(0, 0)
					if before.PeakCores != 0 || before.SustainedCores != nil || before.PeakMemoryBytes != 0 || before.CPUMeasured {
						t.Fatalf("stale measurements exposed: %+v", before)
					}
					obs := store.ProfileObservation{Duration: time.Second, PeakCores: 2, SustainedCores: new(float64(0)), PeakMemoryBytes: 1024, CPUMeasured: true, PlanHash: "new", Contended: contended}
					if contended {
						obs.FloorCores = 1
					}
					if err := st.RecordProfileObservation(t.Context(), "parcels", "sort", obs); err != nil {
						t.Fatal(err)
					}
					if contended {
						after := check(0, 1)
						if after.PeakCores != 0 || after.SustainedCores != nil {
							t.Fatalf("contention invented a measured profile: %+v", after)
						}
					} else {
						after := check(1, 0)
						if after.PeakCores != 2 || after.SustainedCores == nil || *after.SustainedCores != 0 || after.PeakMemoryBytes != 1024 {
							t.Fatalf("fresh observation changed: %+v", after)
						}
					}
				})
			}
		}
	}
}
