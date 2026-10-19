package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestFoldRunProfilesPartialSamplesRaiseOnly(t *testing.T) {
	cases := []struct {
		name      string
		marker    store.MetricKind
		cores     int64
		memory    int64
		wantCount int
		wantCores float64
		wantMem   int64
	}{
		{"partial above raises", store.MetricPartial, 2000, 2000, 1, 2, 2000},
		{"partial below leaves the profile", store.MetricPartial, 500, 500, 1, 1, 1000},
		{"unknown stays excluded", store.MetricUnknown, 2000, 2000, 1, 1, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := t.Context()
			start := time.Now().Add(-time.Minute)
			if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{ID: "partial", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := teststore.SeedNode(st, ctx, store.Node{RunID: "partial", NodeID: "build", Status: "running", StartedAt: &start}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"build", ""} {
				if err := teststore.ForTeam(t, st, store.DefaultTeam).RecordProfileObservation(ctx, "demo", id, store.ProfileObservation{CPUMeasured: true, Duration: time.Minute, PeakCores: 1, SustainedCores: 1, PeakMemoryBytes: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			for i, sample := range []store.MetricSample{
				{Kind: store.MetricInterval, TS: start, CPUMillicores: tc.cores, MemoryBytes: tc.memory},
				{Kind: store.MetricInterval, TS: start.Add(2 * time.Second), CPUMillicores: tc.cores, MemoryBytes: tc.memory},
				{Kind: tc.marker, TS: start.Add(4 * time.Second)},
			} {
				if err := teststore.SeedMetric(teststore.SQL(t, st), ctx, "partial", "build", sample); err != nil {
					t.Fatalf("sample %d: %v", i, err)
				}
			}
			if err := teststore.SeedNodeFinish(st, ctx, store.DefaultTeam, store.NodeRef{RunID: "partial", NodeID: "build"}, store.NodeResult{Outcome: store.NodeState("success"), MaskedError: ""}); err != nil {
				t.Fatal(err)
			}
			if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, "partial", "success", ""); err != nil {
				t.Fatal(err)
			}
			tenant := teststore.ForTeam(t, st, store.DefaultTeam)
			run, err := tenant.GetRun(ctx, "partial")
			if err != nil {
				t.Fatal(err)
			}
			foldProfilesForTest(t, tenant, run)
			for _, id := range []string{"build", ""} {
				p, err := tenant.GetPipelineProfile(ctx, "demo", id)
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != tc.wantCount || p.PeakCores != tc.wantCores || p.PeakMemoryBytes != tc.wantMem {
					t.Fatalf("profile %q = %+v, want count %d cores %v memory %d", id, p, tc.wantCount, tc.wantCores, tc.wantMem)
				}
			}
		})
	}
}

func foldProfilesForTest(t *testing.T, tenant *store.Tenant, run *store.Run) {
	t.Helper()
	observations, err := runProfileObservations(t.Context(), tenant, run)
	if err != nil {
		t.Fatal(err)
	}
	for node, observation := range observations {
		if err := tenant.RecordProfileObservation(t.Context(), run.Pipeline, node, observation); err != nil {
			t.Fatal(err)
		}
	}
}
