package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestProfileHistory(t *testing.T) {
	for _, node := range []string{"", "worker"} {
		for _, document := range []struct {
			name string
			raw  []byte
		}{
			{"schema-three", []byte(`{"schema":3,"samples":[{"d":9000000000,"c":14,"m":450}]}`)},
			{"schema-five", []byte(`{"schema":5,"samples":[{"d":9000000000,"c":14,"s":12,"m":450}]}`)},
			{"empty-schema-five", []byte(`{"schema":5,"samples":[]}`)},
			{"schema-four", []byte(`{"schema":4,"samples":[{"d":9000000000,"c":14,"s":12,"m":450}]}`)},
			{"empty-window", []byte(`{"schema":4,"samples":[]}`)},
			{"future-schema", []byte(`{"schema":999,"samples":[{"d":9000000000,"c":14,"s":12,"m":450}]}`)},
			{"malformed", []byte(`{`)},
			{"empty", []byte{}},
			{"null", nil},
		} {
			t.Run(node+"/"+document.name, func(t *testing.T) {
				st := storetest.Open(t)
				ctx := context.Background()
				for _, count := range []int{0, 1} {
					seed := func() {
						t.Helper()
						if _, err := st.DB().Exec(storetest.Rebind(st, `DELETE FROM pipeline_profiles WHERE pipeline = ?`), "history"); err != nil {
							t.Fatal(err)
						}
						_, err := st.DB().Exec(storetest.Rebind(st, `INSERT INTO pipeline_profiles
 (pipeline,node_id,p50_duration_ms,p99_duration_ms,peak_cores,peak_memory_bytes,sample_count,cpu_measured,updated_at,samples_json,
 plan_hash,floor_cores,floor_memory_bytes,prev_peak_cores,prev_peak_memory_bytes,sustained_cores,prev_sustained_cores,
 pinned_cores,pinned_memory_bytes,wait_p50_ms,wait_p99_ms,wait_sample_count,contended_count)
 VALUES (?,?,9000,9000,14,450,?,1,1,?,'old-plan',10,400,16,500,12,13,2,100,3,7,2,6)`), "history", node, count, document.raw)
						if err != nil {
							t.Fatal(err)
						}
					}
					check := func(p *store.PipelineProfile, samples int, floor float64, memory int64) {
						t.Helper()
						if p == nil {
							t.Fatal("missing profile")
						}
						if p.SampleCount != samples || p.FloorCores != floor || p.FloorMemoryBytes != memory ||
							p.PrevPeakCores != 0 || p.PrevSustainedCores != 0 || p.PrevPeakMemoryBytes != 0 {
							t.Errorf("history survived: %+v", p)
						}
						if p.PinnedCores != 2 || p.PinnedMemoryBytes != 100 || p.WaitP50 != 3*time.Millisecond || p.WaitP99 != 7*time.Millisecond || p.WaitSampleCount != 2 || p.ContendedCount != 6 {
							t.Errorf("pin or wait changed: %+v", p)
						}
						if samples == 0 && (p.PeakCores != 0 || p.SustainedCores != 0 || p.PeakMemoryBytes != 0 || p.P50Duration != 0 || p.P99Duration != 0 || p.CPUP50 != 0 || p.MemoryP95Bytes != 0) {
							t.Errorf("obsolete measured values survived: %+v", p)
						}
					}
					seed()
					p, err := st.GetPipelineProfile(ctx, "history", node)
					if err != nil {
						t.Fatal(err)
					}
					check(p, 0, 0, 0)
					if p.CPUMeasured || p.PlanHash != "" {
						t.Errorf("obsolete measurement metadata survived: %+v", p)
					}
					profiles, err := st.ListPipelineProfiles(ctx, "history")
					if err != nil || len(profiles) != 1 {
						t.Fatalf("list: %v, %d profiles", err, len(profiles))
					}
					check(&profiles[0], 0, 0, 0)
					if profiles[0].CPUMeasured || profiles[0].PlanHash != "" {
						t.Errorf("obsolete list metadata survived: %+v", profiles[0])
					}
					window, err := st.ProfileSamples(ctx, "history", node)
					if err != nil || len(window) != 0 {
						t.Errorf("obsolete samples: %v, %v", window, err)
					}
					if err := st.UpsertProfilePin(ctx, "history", node, 2, 100); err != nil {
						t.Fatal(err)
					}
					if node == "" {
						if err := st.RecordWaitObservation(ctx, "history", time.Millisecond); err != nil {
							t.Fatal(err)
						}
					}
					var after []byte
					if err := st.DB().QueryRow(storetest.Rebind(st, `SELECT samples_json FROM pipeline_profiles WHERE pipeline = ? AND node_id = ?`), "history", node).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if string(after) != string(document.raw) {
						t.Errorf("pin or wait update changed resource document: %s", after)
					}
					for _, hash := range []string{"old-plan", "new-plan"} {
						for _, contended := range []bool{false, true} {
							seed()
							obs := store.ProfileObservation{PlanHash: hash, Duration: time.Second, PeakCores: 3, SustainedCores: 1, PeakMemoryBytes: 80, Contended: contended, FloorCores: 2, FloorMemoryBytes: 60, CPUMeasured: true}
							if err := st.RecordProfileObservation(ctx, "history", node, obs); err != nil {
								t.Fatal(err)
							}
							p, err := st.GetPipelineProfile(ctx, "history", node)
							if err != nil {
								t.Fatal(err)
							}
							if contended {
								check(p, 0, 2, 60)
								if err := st.RecordProfileObservation(ctx, "history", node, store.ProfileObservation{PlanHash: hash, Contended: true}); err != nil {
									t.Fatal(err)
								}
								p, err = st.GetPipelineProfile(ctx, "history", node)
								if err != nil {
									t.Fatal(err)
								}
								check(p, 0, 1, 30)
							} else {
								check(p, 1, 0, 0)
								if p.PeakCores != 3 || p.SustainedCores != 1 || p.PeakMemoryBytes != 80 || p.P50Duration != time.Second {
									t.Errorf("new observation differs: %+v", p)
								}
							}
							if p.PlanHash != hash || !p.CPUMeasured {
								t.Errorf("new observation metadata differs: %+v", p)
							}
						}
					}
				}
			})
		}
	}
}
