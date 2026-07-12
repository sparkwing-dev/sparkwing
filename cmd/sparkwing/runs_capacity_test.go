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

// TestGroupCapacityStats_CarriesDistributionFields pins that grouping
// keeps the rollup's percentile fields intact for the JSON view.
func TestGroupCapacityStats_CarriesDistributionFields(t *testing.T) {
	stats := groupCapacityStats([]store.PipelineProfile{
		{Pipeline: "build", NodeID: "", CPUP50: 1, CPUP95: 2, PeakCores: 3,
			WaitP50: time.Second, WaitP99: 5 * time.Second, WaitSampleCount: 4, SampleCount: 10},
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
