package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type capacityStat struct {
	Pipeline       string                  `json:"pipeline"`
	Source         string                  `json:"source"`
	Drift          string                  `json:"drift,omitempty"`
	CachedExcluded int                     `json:"cached_excluded,omitempty"`
	Rollup         store.PipelineProfile   `json:"rollup"`
	Nodes          []store.PipelineProfile `json:"nodes,omitempty"`
}

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
	if len(profiles) == 0 && pipeline != "" {
		all, err := st.ListPipelineProfiles(ctx, "")
		if err != nil {
			return err
		}
		profiles = matchProfileName(all, pipeline)
	}
	stats := groupCapacityStats(profiles)
	cachedExcluded, err := st.CacheExcludedCounts(ctx, barePipeline(pipeline), string(sparkwing.Cached), capacity.CacheDominantFraction)
	if err != nil {
		return err
	}
	for i := range stats {
		n, ok := cachedExcluded[stats[i].Pipeline]
		if !ok {
			n = cachedExcluded[barePipeline(stats[i].Pipeline)]
		}
		stats[i].CachedExcluded = n
	}

	if emitJSON {
		return ndjson.Write(os.Stdout, stats)
	}
	if len(stats) == 0 {
		fmt.Println("no measured capacity profiles yet; run a pipeline a few times to build one")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tSOURCE\tP50\tP99\tCPU P50/P95/PEAK\tCPU CHARGE\tMEM P50/P95/PEAK\tWAIT P50/P99\tSAMPLES\tCONTENDED\tCACHED\tFLOOR")
	for _, s := range stats {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			store.DisplayProfileKey(s.Pipeline), s.Source, fmtDur(s.Rollup.P50Duration), fmtDur(s.Rollup.P99Duration),
			fmtCPUCells(s.Rollup), fmtCPUChargeCell(s.Rollup), fmtMemCells(s.Rollup),
			fmtWaitCells(s.Rollup), s.Rollup.SampleCount,
			fmtContendedCell(s.Rollup), fmtCachedCell(s.CachedExcluded), fmtFloorCell(s.Rollup))
		for _, n := range s.Nodes {
			fmt.Fprintf(tw, "  %s\t\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
				n.NodeID, fmtDur(n.P50Duration), fmtDur(n.P99Duration),
				fmtCPUCells(n), fmtCPUChargeCell(n), fmtMemCells(n), "-", n.SampleCount, "-", "-", "-")
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range stats {
		if s.Drift != "" {
			fmt.Fprintf(os.Stdout, "\n%s: %s\n", store.DisplayProfileKey(s.Pipeline), s.Drift)
		}
	}
	return nil
}

func runCapacityReset(ctx context.Context, paths orchestrator.Paths, pipeline string, resetAll, yes, emitJSON bool) error {
	switch {
	case resetAll && pipeline != "":
		return fmt.Errorf("runs stats --reset: pass --pipeline NAME or --all, not both")
	case !resetAll && pipeline == "":
		return fmt.Errorf("runs stats --reset: name a pipeline with --pipeline NAME, or reset every pipeline with --all --yes")
	case resetAll && !yes:
		return fmt.Errorf("runs stats --reset --all resets every pipeline's learned profile; re-run with --yes to confirm")
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	var summary store.ProfileResetSummary
	if resetAll {
		summary, err = st.ResetAllProfiles(ctx)
	} else {
		summary, err = resetNamedProfile(ctx, st, pipeline)
	}
	if err != nil {
		return err
	}
	if emitJSON {
		return jsonEncode(os.Stdout, summary)
	}
	if summary.RowsDeleted == 0 && summary.RowsCleared == 0 {
		if resetAll {
			fmt.Println("no stored capacity profiles to reset")
		} else {
			fmt.Printf("nothing stored under %q to reset: no measured samples and no demand floor\n", pipeline)
			fmt.Println("run `sparkwing runs stats --capacity` for the keys that do exist")
		}
		return nil
	}
	displayed := make([]string, len(summary.Pipelines))
	for i, key := range summary.Pipelines {
		displayed[i] = store.DisplayProfileKey(key)
	}
	scope := strings.Join(displayed, ", ")
	if resetAll {
		scope = fmt.Sprintf("%d pipeline(s)", len(summary.Pipelines))
	}
	fmt.Printf("reset %s: dropped %d row(s) and cleared %d pinned row(s), discarding %d learned sample(s) and %d demand floor(s)\n",
		scope, summary.RowsDeleted, summary.RowsCleared, summary.SamplesDropped, summary.FloorsDropped)
	if summary.FloorsDropped > 0 {
		fmt.Println("a dropped demand floor was pricing runs on its own; the next run is charged the cold-start default instead")
	}
	if summary.RowsCleared > 0 {
		fmt.Println("pins were kept; those pipelines re-learn from cold start while admission keeps charging the pin")
	}
	return nil
}

func resetNamedProfile(ctx context.Context, st *store.Store, name string) (store.ProfileResetSummary, error) {
	all, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		return store.ProfileResetSummary{}, err
	}
	total := store.ProfileResetSummary{Pipelines: []string{}}
	done := map[string]bool{}
	for _, p := range matchProfileName(all, name) {
		if done[p.Pipeline] {
			continue
		}
		done[p.Pipeline] = true
		one, err := st.ResetPipelineProfile(ctx, p.Pipeline)
		if err != nil {
			return store.ProfileResetSummary{}, err
		}
		total.Pipelines = append(total.Pipelines, one.Pipelines...)
		total.RowsDeleted += one.RowsDeleted
		total.RowsCleared += one.RowsCleared
		total.SamplesDropped += one.SamplesDropped
		total.FloorsDropped += one.FloorsDropped
	}
	return total, nil
}

func barePipeline(key string) string {
	_, pipeline := store.SplitProfileKey(key)
	return pipeline
}

func matchProfileName(profiles []store.PipelineProfile, name string) []store.PipelineProfile {
	var out []store.PipelineProfile
	for _, p := range profiles {
		if p.Pipeline == name || barePipeline(p.Pipeline) == name || store.DisplayProfileKey(p.Pipeline) == name {
			out = append(out, p)
		}
	}
	return out
}

func fmtCPUCells(p store.PipelineProfile) string {
	return fmt.Sprintf("%.1f/%.1f/%.1f", p.CPUP50, p.CPUP95, p.PeakCores)
}

func fmtCPUChargeCell(p store.PipelineProfile) string {
	charge := p.SustainedCores
	if charge == 0 {
		charge = p.PeakCores
	}
	return fmt.Sprintf("%.1f", charge)
}

func fmtMemCells(p store.PipelineProfile) string {
	return fmt.Sprintf("%s/%s/%s",
		humanBytes(p.MemoryP50Bytes), humanBytes(p.MemoryP95Bytes), humanBytes(p.PeakMemoryBytes))
}

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

func fmtCachedCell(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func fmtFloorCell(p store.PipelineProfile) string {
	if p.FloorCores <= 0 && p.FloorMemoryBytes <= 0 {
		return "-"
	}
	if p.FloorMemoryBytes <= 0 {
		return fmt.Sprintf("%.1f", p.FloorCores)
	}
	return fmt.Sprintf("%.1f/%s", p.FloorCores, humanBytes(p.FloorMemoryBytes))
}

func fmtWaitCells(p store.PipelineProfile) string {
	if p.WaitSampleCount == 0 {
		return "-"
	}
	return fmtDur(p.WaitP50) + "/" + fmtDur(p.WaitP99)
}

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

func deriveSource(rollup store.PipelineProfile) store.CostSource {
	if rollup.PinnedCores > 0 || rollup.PinnedMemoryBytes > 0 {
		return store.CostSourcePin
	}
	if rollup.SampleCount >= capacity.MinSamples && (rollup.PeakCores > 0 || rollup.CPUMeasured) {
		return store.CostSourceMeasured
	}
	if rollup.FloorCores > 0 {
		return store.CostSourceFloor
	}
	if rollup.PrevPeakCores > 0 {
		return store.CostSourceMeasuring
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
