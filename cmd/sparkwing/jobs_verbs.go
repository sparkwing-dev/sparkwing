package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type failureRow struct {
	ID        string    `json:"id"`
	Pipeline  string    `json:"pipeline"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Step      string    `json:"step,omitempty"`
	Message   string    `json:"message,omitempty"`
	Store     string    `json:"store"`
}

func (f failureRow) clusterKey(groupBy string) string {
	switch groupBy {
	case "step", "node":
		if f.Step != "" {
			return f.Step
		}
		return "(unknown)"
	default:
		if f.Step != "" {
			return "step:" + f.Step
		}
		return "(unknown)"
	}
}

func collectLocalFailures(
	ctx context.Context,
	paths orchestrator.Paths,
	filter store.RunFilter,
	limit int,
	keep func(*store.Run) bool,
) ([]failureRow, []string, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = st.Close() }()

	runs, err := st.ListRuns(ctx, filter)
	if err != nil {
		return nil, nil, err
	}
	merged := orchestrator.TagShared(runs)

	standalone := orchestrator.OpenStandaloneStores(ctx, paths)
	defer func() { _ = standalone.Close() }()
	merged = orchestrator.MergeTaggedRuns(append(merged, standalone.ListRuns(ctx, filter)...))

	rows := make([]failureRow, 0, len(merged))
	for _, r := range merged {
		if limit > 0 && len(rows) == limit {
			break
		}
		if !keep(r.Run) {
			continue
		}
		rows = append(rows, failureRowFor(ctx, standalone, st, r))
	}
	return rows, standalone.Notes(), nil
}

func failureRowFor(
	ctx context.Context,
	standalone *orchestrator.StandaloneStores,
	shared *store.Store,
	r orchestrator.TaggedRun,
) failureRow {
	row := failureRow{
		ID: r.ID, Pipeline: r.Pipeline, CreatedAt: r.StartedAt, Status: r.Status, Store: r.Store,
	}
	holder := shared
	if r.Store != orchestrator.SharedStoreLabel {
		st, ok := standalone.StoreFor(r.Store)
		if !ok {
			return row
		}
		holder = st
	}
	if nodes, err := holder.ListNodes(ctx, r.ID); err == nil {
		for _, n := range nodes {
			if n.Outcome == "failed" && n.Error != "" && n.Error != "upstream-failed" {
				row.Step = n.NodeID
				row.Message = truncateOneLine(n.Error, 160)
				break
			}
		}
	}
	if row.Message == "" && r.Error != "" {
		row.Message = truncateOneLine(r.Error, 160)
	}
	return row
}

func collectRemoteFailures(ctx context.Context, controllerURL, token string, filter store.RunFilter, limit int, keep func(*store.Run) bool) ([]failureRow, error) {
	c := client.NewWithToken(controllerURL, nil, token)
	runs, err := c.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows := make([]failureRow, 0, len(runs))
	for _, r := range runs {
		if limit > 0 && len(rows) == limit {
			break
		}
		if !keep(r) {
			continue
		}
		row := failureRow{
			ID: r.ID, Pipeline: r.Pipeline, CreatedAt: r.StartedAt, Status: r.Status,
			Store: orchestrator.SharedStoreLabel,
		}
		nodes, err := c.ListNodes(ctx, r.ID)
		if err == nil {
			for _, n := range nodes {
				if n.Outcome == "failed" && n.Error != "" && n.Error != "upstream-failed" {
					row.Step = n.NodeID
					row.Message = truncateOneLine(n.Error, 160)
					break
				}
			}
		}
		if row.Message == "" && r.Error != "" {
			row.Message = truncateOneLine(r.Error, 160)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func renderFailures(rows []failureRow, groupBy string, asJSON bool) error {
	if groupBy != "" {
		return renderFailureClusters(rows, groupBy, asJSON)
	}
	if asJSON {
		return ndjson.Write(os.Stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Println("no failures found")
		return nil
	}
	header := "ID\tPIPELINE\tWHEN\tSTEP\tERROR"
	showStore := false
	for _, r := range rows {
		if r.Store != "" && r.Store != orchestrator.SharedStoreLabel {
			showStore = true
			break
		}
	}
	if showStore {
		header += "\tSTORE"
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			r.ID, r.Pipeline, relTime(r.CreatedAt),
			dashIfEmpty(r.Step), dashIfEmpty(r.Message))
		if showStore {
			line += "\t" + r.Store
		}
		fmt.Fprintln(tw, line)
	}
	return tw.Flush()
}

func renderFailureClusters(rows []failureRow, groupBy string, asJSON bool) error {
	type cluster struct {
		Key         string    `json:"key"`
		Count       int       `json:"count"`
		First       time.Time `json:"first"`
		Last        time.Time `json:"last"`
		SampleError string    `json:"sample_error,omitempty"`
	}
	byKey := map[string]*cluster{}
	for _, r := range rows {
		k := r.clusterKey(groupBy)
		c, ok := byKey[k]
		if !ok {
			c = &cluster{Key: k, First: r.CreatedAt, Last: r.CreatedAt}
			byKey[k] = c
		}
		c.Count++
		if r.CreatedAt.Before(c.First) {
			c.First = r.CreatedAt
		}
		if r.CreatedAt.After(c.Last) {
			c.Last = r.CreatedAt
		}
		if c.SampleError == "" && r.Message != "" {
			c.SampleError = r.Message
		}
	}
	clusters := make([]*cluster, 0, len(byKey))
	for _, c := range byKey {
		clusters = append(clusters, c)
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].Count != clusters[j].Count {
			return clusters[i].Count > clusters[j].Count
		}
		return clusters[i].Last.After(clusters[j].Last)
	})
	if asJSON {
		return ndjson.Write(os.Stdout, clusters)
	}
	if len(clusters) == 0 {
		fmt.Println("no failures found")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tCOUNT\tFIRST\tLAST\tSAMPLE")
	for _, c := range clusters {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n",
			c.Key, c.Count, relTime(c.First), relTime(c.Last), dashIfEmpty(c.SampleError))
	}
	return tw.Flush()
}

type pipelineStats struct {
	Pipeline   string        `json:"pipeline"`
	Runs       int           `json:"runs"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Running    int           `json:"running"`
	SuccessPct float64       `json:"success_pct"`
	AvgDur     time.Duration `json:"avg_duration_ns"`
	P95Dur     time.Duration `json:"p95_duration_ns"`
}

func runJobsStats(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsStats.Path, flag.ContinueOnError)
	on := fs.String("profile", "", "read against the named profile (default: $SPARKWING_PROFILE, then the project's defaults.profile)")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	since := lookbackDuration(fs, "since", 0, "only runs newer than this (e.g. 7d)")
	capacityView := fs.Bool("capacity", false, "show measured capacity profiles")
	reset := fs.Bool("reset", false, "delete a pipeline's learned capacity profile so it re-learns (keeps pins)")
	resetAll := fs.Bool("all", false, "with --reset, reset every pipeline")
	yes := fs.Bool("yes", false, "confirm --reset --all")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdJobsStats, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, "runs stats")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"
	if *reset {
		return runCapacityReset(ctx, paths, *pipeline, *resetAll, *yes, emitJSON)
	}
	if *capacityView {
		return runCapacityStats(ctx, paths, *pipeline, emitJSON)
	}
	var runs []*store.Run
	var err error
	prof, perr := resolveProfileFlag(*on)
	if perr != nil {
		return perr
	}
	if prof != nil && (*on != "" || prof.ControllerURL() != "") {
		if err := requireController(prof, "runs stats"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		filter := store.RunFilter{Limit: 500}
		if *pipeline != "" {
			filter.Pipelines = []string{*pipeline}
		}
		if *since > 0 {
			filter.Since = time.Now().Add(-*since)
		}
		runs, err = c.ListRuns(ctx, filter)
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return err
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return oerr
		}
		defer func() { _ = st.Close() }()
		filter := store.RunFilter{Limit: 500}
		if *pipeline != "" {
			filter.Pipelines = []string{*pipeline}
		}
		if *since > 0 {
			filter.Since = time.Now().Add(-*since)
		}
		runs, err = st.ListRuns(ctx, filter)
	}
	if err != nil {
		return err
	}

	groups := map[string][]*store.Run{}
	for _, r := range runs {
		if r.ParentRunID != "" {
			continue
		}
		groups[r.Pipeline] = append(groups[r.Pipeline], r)
	}
	stats := make([]pipelineStats, 0, len(groups))
	for name, g := range groups {
		stats = append(stats, aggregateRuns(name, g))
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Pipeline < stats[j].Pipeline })

	if emitJSON {
		return ndjson.Write(os.Stdout, stats)
	}
	if len(stats) == 0 {
		fmt.Println("no runs match the filter")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tRUNS\tPASS\tFAIL\tRUN\tSUCCESS\tAVG\tP95")
	for _, s := range stats {
		success := "-"
		if s.Passed+s.Failed > 0 {
			success = fmt.Sprintf("%.0f%%", s.SuccessPct)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
			s.Pipeline, s.Runs, s.Passed, s.Failed, s.Running,
			success, fmtDur(s.AvgDur), fmtDur(s.P95Dur))
	}
	return tw.Flush()
}

func aggregateRuns(name string, runs []*store.Run) pipelineStats {
	s := pipelineStats{Pipeline: name, Runs: len(runs)}
	var durations []time.Duration
	for _, r := range runs {
		switch r.Status {
		case "success":
			s.Passed++
		case "failed":
			s.Failed++
		case "running", "claimed", "pending":
			s.Running++
		}
		if r.FinishedAt != nil {
			durations = append(durations, r.FinishedAt.Sub(r.StartedAt))
		}
	}
	if term := s.Passed + s.Failed; term > 0 {
		s.SuccessPct = float64(s.Passed) / float64(term) * 100
	}
	if len(durations) > 0 {
		var sum time.Duration
		for _, d := range durations {
			sum += d
		}
		s.AvgDur = sum / time.Duration(len(durations))
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		idx := int(float64(len(durations)) * 0.95)
		if idx >= len(durations) {
			idx = len(durations) - 1
		}
		s.P95Dur = durations[idx]
	}
	return s
}

func shortSHAOrDash(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func jsonEncode(w *os.File, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
