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

func runJobsFailures(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsFailures.Path, flag.ContinueOnError)
	on := fs.String("profile", "", "profile name; omit for local-only")
	limit := fs.Int("limit", 20, "max failures to analyze")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	gitSHA := fs.String("git-sha", "", "restrict to a git SHA prefix")
	branch := fs.String("branch", "", "restrict to one git branch")
	repo := fs.String("repo", "", "restrict to one repository (owner/name)")
	since := lookbackDuration(fs, "since", 0, "only failures newer than this (e.g. 24h, 7d)")
	groupBy := fs.String("group-by", "", "cluster failures by: step | node (default: flat list)")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdJobsFailures, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, "runs failures")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	var rows []failureRow
	var notes []string
	var err error
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, "runs failures"); err != nil {
			return err
		}
		rows, err = collectRemoteFailures(ctx, prof.ControllerURL(), prof.ControllerToken(),
			failureRunFilter(*pipeline, *gitSHA, *branch, *repo, *since, *limit))
	} else {
		rows, notes, err = collectLocalFailures(ctx, paths,
			failureRunFilter(*pipeline, *gitSHA, *branch, *repo, *since, *limit), *limit)
	}
	if err != nil {
		return err
	}
	if err := renderFailures(rows, *groupBy, emitJSON); err != nil {
		return err
	}
	orchestrator.WriteStandaloneNotes(os.Stderr, notes)
	return nil
}

func failureRunFilter(pipeline, gitSHA, branch, repo string, since time.Duration, limit int) store.RunFilter {
	f := store.RunFilter{
		Statuses: []string{"failed"},
		RootOnly: true,
		Limit:    limit,
	}
	if gitSHA != "" {
		f.GitSHAPrefixes = []string{gitSHA}
	}
	if pipeline != "" {
		f.Pipelines = []string{pipeline}
	}
	if branch != "" {
		f.GitBranches = []string{branch}
	}
	if repo != "" {
		f.Repos = []string{repo}
	}
	if since > 0 {
		f.Since = time.Now().Add(-since)
	}
	return f
}

func collectLocalFailures(
	ctx context.Context,
	paths orchestrator.Paths,
	filter store.RunFilter,
	limit int,
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
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}

	rows := make([]failureRow, 0, len(merged))
	for _, r := range merged {
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
		if st, _, _, ok := standalone.Find(ctx, r.ID); ok {
			holder = st
		}
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

func collectRemoteFailures(ctx context.Context, controllerURL, token string, filter store.RunFilter) ([]failureRow, error) {
	c := client.NewWithToken(controllerURL, nil, token)
	runs, err := c.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows := make([]failureRow, 0, len(runs))
	for _, r := range runs {
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
	on := fs.String("profile", "", "profile name; omit for local-only")
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
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
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

func runJobsLast(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsLast.Path, flag.ContinueOnError)
	on := fs.String("profile", "", "profile name; omit for local-only")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	watch := fs.BoolP("watch", "w", false, "tail for new runs (reprints whenever a newer run appears)")
	if err := parseAndCheck(cmdJobsLast, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, "runs last")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	fetch := func() (*store.Run, error) {
		filter := store.RunFilter{Limit: 1}
		if *pipeline != "" {
			filter.Pipelines = []string{*pipeline}
		}
		if *on != "" {
			prof, err := resolveProfile(*on)
			if err != nil {
				return nil, err
			}
			if err := requireController(prof, "runs last"); err != nil {
				return nil, err
			}
			c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
			runs, err := c.ListRuns(ctx, filter)
			if err != nil {
				return nil, err
			}
			if len(runs) == 0 {
				return nil, nil
			}
			return runs[0], nil
		}
		if err := paths.EnsureRoot(); err != nil {
			return nil, err
		}
		st, err := store.Open(paths.StateDB())
		if err != nil {
			return nil, err
		}
		defer func() { _ = st.Close() }()
		runs, err := st.ListRuns(ctx, filter)
		if err != nil {
			return nil, err
		}
		if len(runs) == 0 {
			return nil, nil
		}
		return runs[0], nil
	}

	emit := func(r *store.Run) {
		if r == nil {
			fmt.Println("(no runs)")
			return
		}
		if emitJSON {
			_ = jsonEncode(os.Stdout, store.RedactedRun(r))
			return
		}
		fmt.Printf("%s  %s  %s  (%s)\n",
			r.ID, r.Pipeline, r.Status, relTime(r.StartedAt))
	}

	r, err := fetch()
	if err != nil {
		return err
	}
	emit(r)
	if !*watch {
		return nil
	}
	last := ""
	if r != nil {
		last = r.ID
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		next, err := fetch()
		if err != nil {
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
			continue
		}
		if next != nil && next.ID != last {
			emit(next)
			last = next.ID
		}
	}
}

func runJobsTree(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsTree.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "root run identifier")
	on := fs.String("profile", "", "profile name; omit for local-only")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdJobsTree, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, "runs tree")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	type runNode struct {
		Run      *store.Run `json:"run"`
		Children []*runNode `json:"children,omitempty"`
	}

	var fetchChildren func(parentID string) ([]*store.Run, error)
	var root *store.Run
	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "runs tree"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		r, err := c.GetRun(ctx, *runID)
		if err != nil {
			return err
		}
		root = r
		fetchChildren = func(parentID string) ([]*store.Run, error) {
			return c.ListRuns(ctx, store.RunFilter{ParentRunID: parentID, Limit: 1000})
		}
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return err
		}
		st, err := store.Open(paths.StateDB())
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		r, err := st.GetRun(ctx, *runID)
		if err != nil {
			return err
		}
		root = r
		fetchChildren = func(parentID string) ([]*store.Run, error) {
			return st.ListRuns(ctx, store.RunFilter{ParentRunID: parentID, Limit: 1000})
		}
	}

	var build func(r *store.Run) (*runNode, error)
	build = func(r *store.Run) (*runNode, error) {
		node := &runNode{Run: store.RedactedRun(r)}
		kids, err := fetchChildren(r.ID)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			child, err := build(k)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		}
		return node, nil
	}
	tree, err := build(root)
	if err != nil {
		return err
	}

	if emitJSON {
		return jsonEncode(os.Stdout, tree)
	}
	var render func(n *runNode, prefix string, last bool)
	render = func(n *runNode, prefix string, last bool) {
		connector := "├── "
		if last {
			connector = "└── "
		}
		if prefix == "" {
			fmt.Printf("%s  %s  %s  (%s)\n",
				n.Run.ID, n.Run.Pipeline, n.Run.Status, relTime(n.Run.StartedAt))
		} else {
			fmt.Printf("%s%s%s  %s  %s  (%s)\n",
				prefix, connector, n.Run.ID, n.Run.Pipeline, n.Run.Status, relTime(n.Run.StartedAt))
		}
		for i, c := range n.Children {
			var next string
			switch {
			case prefix == "":
				next = "    "
			case last:
				next = prefix + "    "
			default:
				next = prefix + "│   "
			}
			render(c, next, i == len(n.Children)-1)
		}
	}
	render(tree, "", true)
	return nil
}

func runJobsGet(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsGet.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	on := fs.String("profile", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdJobsGet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "runs get"); err != nil {
			return err
		}
		return orchestrator.GetRunJSONRemote(ctx, prof.ControllerURL(), prof.ControllerToken(), *runID, os.Stdout)
	}
	return orchestrator.GetRunJSONLocal(ctx, paths, *runID, os.Stdout)
}

func runJobsWait(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsWait.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier to wait on")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up (exit 2) after this long")
	poll := fs.Duration("poll", 3*time.Second, "poll interval")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	on := fs.String("profile", "", "profile name (cluster mode). Omit to poll the local store.")
	if err := parseAndCheck(cmdJobsWait, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, err := resolveOutputFormat(*outFmt, "runs wait")
	if err != nil {
		return err
	}
	if *poll <= 0 {
		return fmt.Errorf("runs wait: --poll must be > 0")
	}

	var fetch func() (*store.Run, error)
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return exitError(4, perr)
		}
		if err := requireController(prof, "runs wait"); err != nil {
			return exitError(4, err)
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		fetch = func() (*store.Run, error) { return c.GetRun(ctx, *runID) }
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return exitError(4, err)
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return exitError(4, oerr)
		}
		defer func() { _ = st.Close() }()
		fetch = func() (*store.Run, error) { return st.GetRun(ctx, *runID) }
	}

	deadline := time.Now().Add(*timeout)
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()

	run, ferr := fetch()
	for {
		if ferr != nil {
			return exitError(3, ferr)
		}
		if run != nil && isTerminalRunStatus(run.Status) {
			emitWaitResult(run, resolvedFmt)
			if run.Status == "success" {
				return nil
			}
			return exitErrorf(1, "run %s: %s", run.ID, run.Status)
		}
		if time.Now().After(deadline) {
			return exitErrorf(2, "runs wait: timeout after %s waiting on %s", *timeout, *runID)
		}
		select {
		case <-ctx.Done():
			return exitError(4, ctx.Err())
		case <-ticker.C:
		}
		run, ferr = fetch()
	}
}

func emitWaitResult(run *store.Run, format string) {
	if run == nil {
		return
	}
	switch format {
	case "json":
		_ = jsonEncode(os.Stdout, store.RedactedRun(run))
	default:
		dur := ""
		if run.FinishedAt != nil {
			dur = run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String()
		}
		fmt.Fprintf(os.Stdout, "%s  %s  %s  %s\n",
			run.ID, run.Pipeline, run.Status, dashIfEmpty(dur))
	}
}

func runJobsFind(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsFind.Path, flag.ContinueOnError)
	gitSHA := fs.String("git-sha", "", "match runs whose git SHA starts with this (prefix match)")
	branch := fs.String("branch", "", "restrict to one git branch")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	repo := fs.String("repo", "", "restrict to one repository (owner/name)")
	rootOnly := fs.Bool("root-only", false, "exclude child runs")
	since := fs.Duration("since", time.Hour, "lookback window (default 1h)")
	limit := fs.Int("limit", 20, "max results")
	wait := fs.Bool("wait", false, "block until at least one match appears")
	findTimeout := fs.Duration("find-timeout", 2*time.Minute, "give up after this long when --wait is set")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	quiet := fs.BoolP("quiet", "q", false, "print only run ids, one per line")
	on := fs.String("profile", "", "profile name (cluster mode). Omit to search local.")
	if err := parseAndCheck(cmdJobsFind, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *gitSHA == "" && *branch == "" && *pipeline == "" && *repo == "" {
		return fmt.Errorf("runs find: at least one of --git-sha, --branch, --pipeline, or --repo is required")
	}
	resolvedFmt, err := resolveOutputFormat(*outFmt, "runs find")
	if err != nil {
		return err
	}

	var searchOnce func() ([]orchestrator.TaggedRun, error)
	var notes func() []string
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, "runs find"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		searchOnce = func() ([]orchestrator.TaggedRun, error) {
			runs, err := c.ListRuns(ctx, findRunFilter(*gitSHA, *branch, *pipeline, *repo, *rootOnly, *since, *limit))
			if err != nil {
				return nil, err
			}
			return orchestrator.TagShared(runs), nil
		}
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return err
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return oerr
		}
		defer func() { _ = st.Close() }()
		standalone := orchestrator.OpenStandaloneStores(ctx, paths)
		defer func() { _ = standalone.Close() }()
		notes = standalone.Notes
		searchOnce = func() ([]orchestrator.TaggedRun, error) {
			filter := findRunFilter(*gitSHA, *branch, *pipeline, *repo, *rootOnly, *since, *limit)
			runs, err := st.ListRuns(ctx, filter)
			if err != nil {
				return nil, err
			}
			merged := orchestrator.MergeTaggedRuns(
				append(orchestrator.TagShared(runs), standalone.ListRuns(ctx, filter)...))
			if *limit > 0 && len(merged) > *limit {
				merged = merged[:*limit]
			}
			return merged, nil
		}
	}

	runs, err := searchOnce()
	if err != nil {
		return err
	}
	if len(runs) == 0 && *wait {
		deadline := time.Now().Add(*findTimeout)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for len(runs) == 0 {
			if time.Now().After(deadline) {
				return exitErrorf(2, "runs find: timeout after %s with no match", *findTimeout)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			runs, err = searchOnce()
			if err != nil {
				return err
			}
		}
	}
	if err := renderFindResults(runs, resolvedFmt, *quiet); err != nil {
		return err
	}
	if notes != nil {
		orchestrator.WriteStandaloneNotes(os.Stderr, notes())
	}
	return nil
}

func findRunFilter(gitSHA, branch, pipeline, repo string, rootOnly bool, since time.Duration, limit int) store.RunFilter {
	filter := store.RunFilter{RootOnly: rootOnly, Limit: limit}
	if gitSHA != "" {
		filter.GitSHAPrefixes = []string{gitSHA}
	}
	if pipeline != "" {
		filter.Pipelines = []string{pipeline}
	}
	if branch != "" {
		filter.GitBranches = []string{branch}
	}
	if repo != "" {
		filter.Repos = []string{repo}
	}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	return filter
}

func renderFindResults(runs []orchestrator.TaggedRun, format string, quiet bool) error {
	if quiet {
		if format == "json" {
			ids := make([]string, 0, len(runs))
			for _, r := range runs {
				ids = append(ids, r.ID)
			}

			return ndjson.Write(os.Stdout, ids)
		}
		for _, r := range runs {
			fmt.Fprintln(os.Stdout, r.ID)
		}
		return nil
	}
	if format == "json" {
		redacted := make([]orchestrator.TaggedRun, 0, len(runs))
		for _, r := range runs {
			redacted = append(redacted, orchestrator.TaggedRun{Run: store.RedactedRun(r.Run), Store: r.Store})
		}
		return ndjson.Write(os.Stdout, redacted)
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stdout, "no runs match the requested filter")
		return nil
	}
	header := "RUN\tPIPELINE\tSTATUS\tSHA\tSTARTED"
	showStore := false
	for _, r := range runs {
		if r.Store != orchestrator.SharedStoreLabel {
			showStore = true
			break
		}
	}
	if showStore {
		header += "\tSTORE"
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range runs {
		line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s",
			r.ID, r.Pipeline, r.Status,
			dashIfEmpty(shortSHAOrDash(r.GitSHA)),
			relTime(r.StartedAt))
		if showStore {
			line += "\t" + r.Store
		}
		fmt.Fprintln(tw, line)
	}
	return tw.Flush()
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
	enc.SetIndent("", "  ")
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
