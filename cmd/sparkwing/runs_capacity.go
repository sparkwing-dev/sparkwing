package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// capacityStat is one pipeline's measured capacity view: the rollup the
// admission charge derives from, its per-node breakdown, the resolved
// source, and any pin-drift note.
type capacityStat struct {
	Pipeline string                  `json:"pipeline"`
	Source   string                  `json:"source"`
	Drift    string                  `json:"drift,omitempty"`
	Rollup   store.PipelineProfile   `json:"rollup"`
	Nodes    []store.PipelineProfile `json:"nodes,omitempty"`
}

// runCapacityStats prints the measured capacity profiles as a table, one
// row per pipeline plus its node breakdown. Any pin-drift warning is
// printed below the table as a per-pipeline footnote rather than inside a
// cell, so its long message never widens or raggeds the aligned columns.
func runCapacityStats(ctx context.Context, paths orchestrator.Paths, pipeline string, emitJSON bool) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	profiles, err := st.ListPipelineProfiles(ctx, pipeline)
	if err != nil {
		return err
	}
	stats := groupCapacityStats(profiles)

	if emitJSON {
		return jsonEncode(os.Stdout, stats)
	}
	if len(stats) == 0 {
		fmt.Println("no measured capacity profiles yet; run a pipeline a few times to build one")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tSOURCE\tP50\tP99\tCPU P50/P95/PEAK\tMEM P50/P95/PEAK\tWAIT P50/P99\tSAMPLES\tCONTENDED")
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			s.Pipeline, s.Source, fmtDur(s.Rollup.P50Duration), fmtDur(s.Rollup.P99Duration),
			fmtCPUCells(s.Rollup), fmtMemCells(s.Rollup), fmtWaitCells(s.Rollup), s.Rollup.SampleCount,
			fmtContendedCell(s.Rollup))
		for _, n := range s.Nodes {
			fmt.Fprintf(tw, "  %s\t\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				n.NodeID, fmtDur(n.P50Duration), fmtDur(n.P99Duration),
				fmtCPUCells(n), fmtMemCells(n), "-", n.SampleCount, "-")
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range stats {
		if s.Drift != "" {
			fmt.Fprintf(os.Stdout, "\n%s: %s\n", s.Pipeline, s.Drift)
		}
	}
	return nil
}

// fmtCPUCells renders a profile's CPU distribution as p50/p95/peak. The
// percentiles describe spikiness; PEAK is what admission charges.
func fmtCPUCells(p store.PipelineProfile) string {
	return fmt.Sprintf("%.1f/%.1f/%.1f", p.CPUP50, p.CPUP95, p.PeakCores)
}

// fmtMemCells renders a profile's memory distribution as p50/p95/peak.
func fmtMemCells(p store.PipelineProfile) string {
	return fmt.Sprintf("%s/%s/%s",
		humanBytes(p.MemoryP50Bytes), humanBytes(p.MemoryP95Bytes), humanBytes(p.PeakMemoryBytes))
}

// fmtContendedCell renders a pipeline's contended share: the count of
// runs the daemon flagged as throttled by host contention over its
// measured runs, with the percentage. A dash before any run is flagged.
func fmtContendedCell(p store.PipelineProfile) string {
	if p.ContendedCount == 0 {
		return "-"
	}
	if p.SampleCount <= 0 {
		return fmt.Sprintf("%d", p.ContendedCount)
	}
	pct := int(float64(p.ContendedCount)/float64(p.SampleCount)*100 + 0.5)
	return fmt.Sprintf("%d/%d (%d%%)", p.ContendedCount, p.SampleCount, pct)
}

// fmtWaitCells renders a rollup's queue-wait percentiles as p50/p99, or
// a dash before any wait has been observed.
func fmtWaitCells(p store.PipelineProfile) string {
	if p.WaitSampleCount == 0 {
		return "-"
	}
	return fmtDur(p.WaitP50) + "/" + fmtDur(p.WaitP99)
}

// groupCapacityStats folds the flat profile rows into per-pipeline stats,
// splitting the rollup (empty node id) from its node rows and deriving the
// resolved source and any pin drift.
func groupCapacityStats(profiles []store.PipelineProfile) []capacityStat {
	byPipeline := map[string]*capacityStat{}
	order := []string{}
	for _, p := range profiles {
		cs, ok := byPipeline[p.Pipeline]
		if !ok {
			cs = &capacityStat{Pipeline: p.Pipeline}
			byPipeline[p.Pipeline] = cs
			order = append(order, p.Pipeline)
		}
		if p.NodeID == "" {
			cs.Rollup = p
		} else {
			cs.Nodes = append(cs.Nodes, p)
		}
	}
	out := make([]capacityStat, 0, len(order))
	for _, name := range order {
		cs := byPipeline[name]
		cs.Source = string(deriveSource(cs.Rollup))
		if d := rollupDrift(cs.Rollup); d != nil {
			cs.Drift = d.Message
		}
		sort.Slice(cs.Nodes, func(i, j int) bool { return cs.Nodes[i].NodeID < cs.Nodes[j].NodeID })
		out = append(out, *cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pipeline < out[j].Pipeline })
	return out
}

// deriveSource reports where a pipeline's admission charge comes from,
// mirroring the resolution order applied at admission time.
func deriveSource(rollup store.PipelineProfile) store.CostSource {
	if rollup.PinnedCores > 0 || rollup.PinnedMemoryBytes > 0 {
		return store.CostSourcePin
	}
	if rollup.SampleCount >= capacity.MinSamples && (rollup.PeakCores > 0 || rollup.CPUMeasured) {
		return store.CostSourceMeasured
	}
	return store.CostSourceDefault
}

func rollupDrift(rollup store.PipelineProfile) *capacity.Drift {
	if rollup.PinnedCores <= 0 && rollup.PinnedMemoryBytes <= 0 {
		return nil
	}
	pin := &capacity.Pin{Cores: rollup.PinnedCores, MemoryBytes: rollup.PinnedMemoryBytes}
	return capacity.CheckDrift(pin, &rollup)
}
