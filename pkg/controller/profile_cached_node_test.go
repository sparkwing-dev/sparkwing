package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFoldProfilesDistinguishesCachedFromUnsampledExecution(t *testing.T) {
	for _, tc := range []struct {
		name           string
		otherOutcome   string
		wantRunProfile bool
	}{
		{"cached node", "cached", true},
		{"unsampled executed node", "success", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			start := time.Unix(1700000000, 0).UTC()
			end := start.Add(2 * time.Second)
			if err := st.CreateRun(ctx, store.Run{ID: "r", Pipeline: "parcels", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"sampled", "other"} {
				if err := st.CreateNode(ctx, store.Node{RunID: "r", NodeID: id, Status: "running"}); err != nil {
					t.Fatal(err)
				}
				outcome := "success"
				if id == "other" {
					outcome = tc.otherOutcome
				}
				if err := st.FinishNode(ctx, "r", id, outcome, "", nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET started_at=?, finished_at=? WHERE run_id='r'`, start.UnixNano(), end.UnixNano()); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().ExecContext(ctx, `UPDATE runs SET status='success', finished_at=? WHERE id='r'`, end.UnixNano()); err != nil {
				t.Fatal(err)
			}
			for _, at := range []time.Time{start, end} {
				if err := st.AddNodeMetricSample(ctx, "r", "sampled", store.MetricSample{
					TS: at, Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true,
					CPUMillicores: 500, MemoryBytes: 4096,
				}); err != nil {
					t.Fatal(err)
				}
			}
			other, err := st.GetNode(ctx, "r", "other")
			if err != nil || other == nil || other.Outcome != tc.otherOutcome {
				t.Fatalf("other node=%+v, error=%v", other, err)
			}
			otherSamples, err := st.ListNodeMetrics(ctx, "r", "other")
			if err != nil || len(otherSamples) != 0 {
				t.Fatalf("other samples=%+v, error=%v", otherSamples, err)
			}
			run, err := st.GetRun(ctx, "r")
			if err != nil || run == nil {
				t.Fatalf("run=%+v, error=%v", run, err)
			}
			New(st, nil).foldRunProfiles(ctx, run)
			for _, id := range []string{"sampled", "other", ""} {
				profile, err := st.GetPipelineProfile(ctx, "parcels", id)
				if err != nil {
					t.Fatal(err)
				}
				want := id == "sampled" || id == "" && tc.wantRunProfile
				if !want {
					if profile != nil {
						t.Errorf("%q unexpectedly acquired a profile: %+v", id, profile)
					}
					continue
				}
				if profile == nil || profile.SampleCount != 1 || !profile.CPUMeasured || profile.PeakCores != 0.5 || profile.PeakMemoryBytes != 4096 || profile.SustainedCores != nil || profile.P50Duration != 2*time.Second {
					t.Errorf("%q lost executed interval evidence: %+v", id, profile)
				}
			}
		})
	}
}
