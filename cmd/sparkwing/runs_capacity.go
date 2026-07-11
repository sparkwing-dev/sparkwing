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
	fmt.Fprintln(tw, "PIPELINE\tSOURCE\tP50\tP99\tPEAK CPU\tPEAK MEM\tSAMPLES")
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.1f\t%s\t%d\n",
			s.Pipeline, s.Source, fmtDur(s.Rollup.P50Duration), fmtDur(s.Rollup.P99Duration),
			s.Rollup.PeakCores, humanBytes(s.Rollup.PeakMemoryBytes), s.Rollup.SampleCount)
		for _, n := range s.Nodes {
			fmt.Fprintf(tw, "  %s\t\t%s\t%s\t%.1f\t%s\t%d\n",
				n.NodeID, fmtDur(n.P50Duration), fmtDur(n.P99Duration),
				n.PeakCores, humanBytes(n.PeakMemoryBytes), n.SampleCount)
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
