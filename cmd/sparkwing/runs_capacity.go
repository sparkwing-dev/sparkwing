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

// capacityStat is one pipeline's measured capacity view: the rollup the
// admission charge derives from, its per-node breakdown, the resolved
// source, and any pin-drift note.
type capacityStat struct {
	Pipeline       string                  `json:"pipeline"`
	Source         string                  `json:"source"`
	Drift          string                  `json:"drift,omitempty"`
	CachedExcluded int                     `json:"cached_excluded,omitempty"`
	Rollup         store.PipelineProfile   `json:"rollup"`
	Nodes          []store.PipelineProfile `json:"nodes,omitempty"`
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
	if len(profiles) == 0 && pipeline != "" && !strings.Contains(pipeline, "/") {
		all, err := st.ListPipelineProfiles(ctx, "")
		if err != nil {
			return err
		}
		profiles = matchBarePipeline(all, pipeline)
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
		// NDJSON: one capacity profile per line.
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
			s.Pipeline, s.Source, fmtDur(s.Rollup.P50Duration), fmtDur(s.Rollup.P99Duration),
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
			fmt.Fprintf(os.Stdout, "\n%s: %s\n", s.Pipeline, s.Drift)
		}
	}
	return nil
}

// runCapacityReset clears learned capacity profiles so a pipeline whose
// measurement went wrong -- a freak run that recorded an absurd peak --
// re-learns from a cold start. Pins are preserved; only the learned
// samples, peaks, waits, and contention tally are dropped. Exactly one of
// pipeline or resetAll selects the scope; the machine-wide reset requires
// yes as a deliberate confirmation.
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
	scope := strings.Join(summary.Pipelines, ", ")
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

// resetNamedProfile resets the profile rows stored under name, falling back
// to every repo-scoped key whose bare pipeline name matches when nothing is
// stored under the name verbatim. Profiles are keyed "repo/pipeline", so an
// operator who types the pipeline name they know -- the name in their own
// source, not the key an internal scoping rule derived -- used to be told
// there was nothing to reset while a ratcheted floor kept pricing their runs.
// Every key actually reset is named in the summary, so the wider reach is
// never silent.
func resetNamedProfile(ctx context.Context, st *store.Store, name string) (store.ProfileResetSummary, error) {
	exact, err := st.ListPipelineProfiles(ctx, name)
	if err != nil {
		return store.ProfileResetSummary{}, err
	}
	if len(exact) > 0 || strings.Contains(name, "/") {
		return st.ResetPipelineProfile(ctx, name)
	}
	all, err := st.ListPipelineProfiles(ctx, "")
	if err != nil {
		return store.ProfileResetSummary{}, err
	}
	total := store.ProfileResetSummary{Pipelines: []string{}}
	done := map[string]bool{}
	for _, p := range matchBarePipeline(all, name) {
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

// barePipeline strips the repo scope from a stored profile key: profiles are
// keyed "repo/pipeline" for runs launched inside a git repo, while run rows
// keep the bare pipeline name the cache-exclusion counts group by. The bare
// count pools same-named pipelines across repos, so it is a fallback for
// display only, never an admission input.
func barePipeline(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[i+1:]
	}
	return key
}

// matchBarePipeline returns the profiles whose bare pipeline name matches
// name, so `--pipeline ci` still finds repo-scoped rows ("myrepo/ci") when
// no row is stored under the bare name. Display convenience only; the
// destructive reset path stays exact-match.
func matchBarePipeline(profiles []store.PipelineProfile, name string) []store.PipelineProfile {
	var out []store.PipelineProfile
	for _, p := range profiles {
		if barePipeline(p.Pipeline) == name {
			out = append(out, p)
		}
	}
	return out
}

// fmtCPUCells renders a profile's CPU distribution as p50/p95/peak. The
// three describe how spiky the pipeline is and none of them is the price;
// fmtCPUChargeCell renders that.
func fmtCPUCells(p store.PipelineProfile) string {
	return fmt.Sprintf("%.1f/%.1f/%.1f", p.CPUP50, p.CPUP95, p.PeakCores)
}

// fmtCPUChargeCell renders the core figure admission actually charges: the
// sustained level, falling back to the peak on a profile measured before
// sustained figures were stored, which is the same fallback the charge
// makes. Without this column the table showed three numbers and the price
// was none of them, leaving an operator to reconcile a queue line against a
// distribution that no longer explained it.
func fmtCPUChargeCell(p store.PipelineProfile) string {
	charge := p.SustainedCores
	if charge == 0 {
		charge = p.PeakCores
	}
	return fmt.Sprintf("%.1f", charge)
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

// fmtCachedCell renders how many finished runs were excluded from learning
// for being cache-dominant (at least CacheDominantFraction of their nodes
// served from cache). A dash before any run is excluded. The count is derived
// from retained run history, so it tracks the runs still in the store.
func fmtCachedCell(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

// fmtFloorCell renders a still-measuring version's demand floor -- the lower
// bound its contended runs proved, which admission charges a safety multiple
// of until a clean run finalizes the price. A dash once the version has
// graduated to a measured peak or never ran under contention.
func fmtFloorCell(p store.PipelineProfile) string {
	if p.FloorCores <= 0 && p.FloorMemoryBytes <= 0 {
		return "-"
	}
	if p.FloorMemoryBytes <= 0 {
		return fmt.Sprintf("%.1f", p.FloorCores)
	}
	return fmt.Sprintf("%.1f/%s", p.FloorCores, humanBytes(p.FloorMemoryBytes))
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
// mirroring the resolution order applied at admission time: a pin wins, then
// a graduated measured profile, then a still-measuring version priced from
// its contended-run floor or a predecessor peak, else the cold-start default.
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
