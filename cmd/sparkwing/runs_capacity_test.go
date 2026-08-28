package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunCapacityReset_RejectsAmbiguousScope(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	ctx := context.Background()
	if err := runCapacityReset(ctx, paths, "build", true, true, false); err == nil {
		t.Error("both --pipeline and --all should be rejected")
	}
	if err := runCapacityReset(ctx, paths, "", false, false, false); err == nil {
		t.Error("neither --pipeline nor --all should be rejected")
	}
	if err := runCapacityReset(ctx, paths, "", true, false, false); err == nil {
		t.Error("--reset --all without --yes should be rejected")
	}
}

func TestRunCapacityReset_DropsProfileAndReportsCounts(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	ctx := context.Background()
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.RecordProfileObservation(ctx, "build", "", store.ProfileObservation{Duration: time.Second, PeakCores: 2, CPUMeasured: true}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = st.Close()

	out := captureStdout(t, func() {
		if err := runCapacityReset(ctx, paths, "build", false, false, false); err != nil {
			t.Fatalf("reset: %v", err)
		}
	})
	if !strings.Contains(out, "dropped 1 row(s)") || !strings.Contains(out, "3 learned sample(s)") {
		t.Errorf("reset output should name the dropped counts, got:\n%s", out)
	}

	st2, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	prof, err := st2.GetPipelineProfile(ctx, "build", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Errorf("profile should be gone after reset, got %+v", prof)
	}
}

// TestRunCapacityReset_ClearsAFloorWithNoSamplesBehindIt drives the state the
// reset verb could not reach: a pipeline that was priced off a contended-run
// demand floor without ever finishing a clean run, so it has a floor and no
// measured samples. The reset must clear the floor and say it did, because a
// line reporting only samples reads as "nothing happened" on exactly the
// profile the operator is trying to unstick.
func TestRunCapacityReset_ClearsAFloorWithNoSamplesBehindIt(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	ctx := context.Background()
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordProfileObservation(ctx, "myrepo/ci", "", store.ProfileObservation{
		CPUMeasured: true, PlanHash: "A", Contended: true,
		FloorCores: 6.5, FloorMemoryBytes: 7 << 30,
	}); err != nil {
		t.Fatalf("seed floor: %v", err)
	}
	seeded, err := st.GetPipelineProfile(ctx, "myrepo/ci", "")
	if err != nil {
		t.Fatal(err)
	}
	if seeded.SampleCount != 0 || seeded.FloorCores == 0 {
		t.Fatalf("seed = %+v, want a floor with no measured samples", seeded)
	}
	_ = st.Close()

	out := captureStdout(t, func() {
		if err := runCapacityReset(ctx, paths, "ci", false, false, false); err != nil {
			t.Fatalf("reset: %v", err)
		}
	})
	if !strings.Contains(out, "1 demand floor(s)") {
		t.Errorf("reset output should name the demand floor it cleared, got:\n%s", out)
	}
	if !strings.Contains(out, "myrepo/ci") {
		t.Errorf("reset output should name the repo-scoped key it reached, got:\n%s", out)
	}

	st2, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	prof, err := st2.GetPipelineProfile(ctx, "myrepo/ci", "")
	if err != nil {
		t.Fatal(err)
	}
	if prof != nil {
		t.Errorf("floor row should be gone after reset, got %+v", prof)
	}
}

func TestRunCapacityReset_SaysNothingIsStoredWhenNothingIs(t *testing.T) {
	paths := orchestrator.PathsAt(t.TempDir())
	ctx := context.Background()
	out := captureStdout(t, func() {
		if err := runCapacityReset(ctx, paths, "ci", false, false, false); err != nil {
			t.Fatalf("reset: %v", err)
		}
	})
	if !strings.Contains(out, `nothing stored under "ci" to reset`) {
		t.Errorf("reset output should say nothing is stored, got:\n%s", out)
	}
	if strings.Contains(out, "demand floor(s)") {
		t.Errorf("reset output should not claim it cleared anything, got:\n%s", out)
	}
}

func TestBarePipeline_StripsRepoScope(t *testing.T) {
	if got := barePipeline("myrepo/ci"); got != "ci" {
		t.Errorf("barePipeline(myrepo/ci) = %q, want ci", got)
	}
	if got := barePipeline("ci"); got != "ci" {
		t.Errorf("barePipeline(ci) = %q, want ci", got)
	}
}

func TestMatchBarePipeline_FindsScopedRowsForBareName(t *testing.T) {
	profiles := []store.PipelineProfile{
		{Pipeline: "alpha/ci"},
		{Pipeline: "beta/ci", NodeID: "build"},
		{Pipeline: "alpha/deploy"},
	}
	got := matchBarePipeline(profiles, "ci")
	if len(got) != 2 {
		t.Fatalf("matched %d profiles, want the two scoped ci rows", len(got))
	}
	if got := matchBarePipeline(profiles, "release"); len(got) != 0 {
		t.Errorf("matched %d profiles for an unknown name, want none", len(got))
	}
}

func TestFmtCPUCells_ShowsDistributionThenPeak(t *testing.T) {
	got := fmtCPUCells(store.PipelineProfile{CPUP50: 0.5, CPUP95: 1.25, PeakCores: 2})
	if got != "0.5/1.2/2.0" {
		t.Errorf("fmtCPUCells = %q, want 0.5/1.2/2.0", got)
	}
}

func TestFmtMemCells_ShowsDistributionThenPeak(t *testing.T) {
	got := fmtMemCells(store.PipelineProfile{
		MemoryP50Bytes: 128 << 20, MemoryP95Bytes: 256 << 20, PeakMemoryBytes: 1 << 30,
	})
	if got != "128.0 MiB/256.0 MiB/1.0 GiB" {
		t.Errorf("fmtMemCells = %q", got)
	}
}

func TestFmtWaitCells_DashBeforeAnyObservation(t *testing.T) {
	if got := fmtWaitCells(store.PipelineProfile{}); got != "-" {
		t.Errorf("no-wait cell = %q, want dash", got)
	}
	got := fmtWaitCells(store.PipelineProfile{
		WaitP50: 4 * time.Second, WaitP99: 2 * time.Minute, WaitSampleCount: 9,
	})
	if got != fmtDur(4*time.Second)+"/"+fmtDur(2*time.Minute) {
		t.Errorf("wait cell = %q", got)
	}
}

func TestGroupCapacityStats_CarriesDistributionFields(t *testing.T) {
	stats := groupCapacityStats([]store.PipelineProfile{
		{
			Pipeline: "build", NodeID: "", CPUP50: 1, CPUP95: 2, PeakCores: 3,
			WaitP50: time.Second, WaitP99: 5 * time.Second, WaitSampleCount: 4, SampleCount: 10,
		},
		{Pipeline: "build", NodeID: "node-a", CPUP50: 0.5, PeakCores: 1, SampleCount: 10},
	})
	if len(stats) != 1 {
		t.Fatalf("stats = %d, want 1", len(stats))
	}
	r := stats[0].Rollup
	if r.CPUP50 != 1 || r.CPUP95 != 2 || r.WaitP99 != 5*time.Second || r.WaitSampleCount != 4 {
		t.Errorf("rollup lost distribution fields: %+v", r)
	}
	if len(stats[0].Nodes) != 1 || stats[0].Nodes[0].CPUP50 != 0.5 {
		t.Errorf("node rows lost distribution fields: %+v", stats[0].Nodes)
	}
}

func TestDeriveSource_MeasuringAndFloorStates(t *testing.T) {
	cases := []struct {
		name   string
		rollup store.PipelineProfile
		want   store.CostSource
	}{
		{"floor from contended runs", store.PipelineProfile{FloorCores: 3, SampleCount: 1, CPUMeasured: true}, store.CostSourceFloor},
		{"measuring after structural change", store.PipelineProfile{PrevPeakCores: 2, SampleCount: 1, CPUMeasured: true}, store.CostSourceMeasuring},
		{"default while gathering first samples", store.PipelineProfile{SampleCount: 1, CPUMeasured: true}, store.CostSourceDefault},
		{"graduated wins over floor", store.PipelineProfile{FloorCores: 3, PeakCores: 4, SampleCount: 3, CPUMeasured: true}, store.CostSourceMeasured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveSource(tc.rollup); got != tc.want {
				t.Errorf("deriveSource = %q, want %q", got, tc.want)
			}
		})
	}
}
