// Handlers for the restored jobs verbs: failures, stats, last, tree,
// get. Each one follows the handler skeleton spelled out in
// help_registry.go: parseAndCheck, resolve --on (optional, defaults
// to local), dispatch.
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

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// --- jobs failures -----------------------------------------------

// failureRow is the normalized view a failure-clustering pass works
// on. Controller-side failure_reason is empty for local runs (that's
// a classification the controller performs), so default clustering
// falls through to step-based grouping.
type failureRow struct {
	ID        string    `json:"id"`
	Pipeline  string    `json:"pipeline"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Step      string    `json:"step,omitempty"`
	Message   string    `json:"message,omitempty"`
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
	on := fs.String("on", "", "profile name (default: current default)")
	limit := fs.Int("limit", 20, "max failures to analyze")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	since := fs.Duration("since", 0, "only failures newer than this (e.g. 24h, 7d)")
	groupBy := fs.String("group-by", "", "cluster failures by: step | node (default: flat list)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	if err := parseAndCheck(cmdJobsFailures, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs failures")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	var rows []failureRow
	var err error
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, "jobs failures"); err != nil {
			return err
		}
		rows, err = collectRemoteFailures(ctx, prof.Controller, prof.Token, *pipeline, *since, *limit)
	} else {
		rows, err = collectLocalFailures(ctx, paths, *pipeline, *since, *limit)
	}
	if err != nil {
		return err
	}
	return renderFailures(rows, *groupBy, emitJSON)
}

func collectLocalFailures(ctx context.Context, paths orchestrator.Paths, pipeline string, since time.Duration, limit int) ([]failureRow, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()

	filter := store.RunFilter{Statuses: []string{"failed"}, Limit: limit * 4}
	if pipeline != "" {
		filter.Pipelines = []string{pipeline}
	}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	runs, err := st.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows := make([]failureRow, 0, len(runs))
	for _, r := range runs {
		if r.ParentRunID != "" {
			continue // only root-level runs
		}
		row := failureRow{ID: r.ID, Pipeline: r.Pipeline, CreatedAt: r.StartedAt, Status: r.Status}
		nodes, err := st.ListNodes(ctx, r.ID)
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
		if len(rows) >= limit {
			break
		}
	}
	return rows, nil
}

func collectRemoteFailures(ctx context.Context, controllerURL, token, pipeline string, since time.Duration, limit int) ([]failureRow, error) {
	c := client.NewWithToken(controllerURL, nil, token)
	filter := store.RunFilter{Statuses: []string{"failed"}, Limit: limit * 4}
	if pipeline != "" {
		filter.Pipelines = []string{pipeline}
	}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	runs, err := c.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows := make([]failureRow, 0, len(runs))
	for _, r := range runs {
		if r.ParentRunID != "" {
			continue
		}
		row := failureRow{ID: r.ID, Pipeline: r.Pipeline, CreatedAt: r.StartedAt, Status: r.Status}
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
		if len(rows) >= limit {
			break
		}
	}
	return rows, nil
}

func renderFailures(rows []failureRow, groupBy string, asJSON bool) error {
	if groupBy != "" {
		return renderFailureClusters(rows, groupBy, asJSON)
	}
	if asJSON {
		if rows == nil {
			rows = []failureRow{}
		}
		return jsonEncode(os.Stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Println("no failures found")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPIPELINE\tWHEN\tSTEP\tERROR")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Pipeline, relTime(r.CreatedAt),
			dashIfEmpty(r.Step), dashIfEmpty(r.Message))
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
		return jsonEncode(os.Stdout, clusters)
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

// --- jobs stats --------------------------------------------------

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
	on := fs.String("on", "", "profile name (default: current default)")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	since := fs.Duration("since", 0, "only runs newer than this (e.g. 7d)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	if err := parseAndCheck(cmdJobsStats, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs stats")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"
	var runs []*store.Run
	var err error
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, "jobs stats"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
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
		defer st.Close()
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
		return jsonEncode(os.Stdout, stats)
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

// --- jobs last ---------------------------------------------------

func runJobsLast(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsLast.Path, flag.ContinueOnError)
	on := fs.String("on", "", "profile name (default: current default)")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	watch := fs.BoolP("watch", "w", false, "tail for new runs (reprints whenever a newer run appears)")
	if err := parseAndCheck(cmdJobsLast, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs last")
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
			if err := requireController(prof, "jobs last"); err != nil {
				return nil, err
			}
			c := client.NewWithToken(prof.Controller, nil, prof.Token)
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
		defer st.Close()
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
			_ = jsonEncode(os.Stdout, r)
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

// --- jobs tree ---------------------------------------------------

func runJobsTree(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsTree.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "root run identifier")
	on := fs.String("on", "", "profile name (default: current default)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	if err := parseAndCheck(cmdJobsTree, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs tree")
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	type runNode struct {
		Run      *store.Run `json:"run"`
		Children []*runNode `json:"children,omitempty"`
	}

	// fetchChildren returns direct children of parentID.
	var fetchChildren func(parentID string) ([]*store.Run, error)
	var root *store.Run
	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "jobs tree"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
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
		defer st.Close()
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
		node := &runNode{Run: r}
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
			next := prefix
			if prefix == "" {
				next = "    "
			} else if last {
				next = prefix + "    "
			} else {
				next = prefix + "│   "
			}
			render(c, next, i == len(n.Children)-1)
		}
	}
	render(tree, "", true)
	return nil
}

// --- jobs get ----------------------------------------------------

func runJobsGet(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsGet.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	on := fs.String("on", "", "profile name (default: current default)")
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
		if err := requireController(prof, "jobs get"); err != nil {
			return err
		}
		return orchestrator.GetRunJSONRemote(ctx, prof.Controller, prof.Token, *runID, os.Stdout)
	}
	return orchestrator.GetRunJSONLocal(ctx, paths, *runID, os.Stdout)
}

// --- jobs wait ---------------------------------------------------

// runJobsWait blocks until the named run reaches a terminal state.
// Exit codes (propagated via cliError):
func runJobsWait(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsWait.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier to wait on")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up (exit 2) after this long")
	poll := fs.Duration("poll", 3*time.Second, "poll interval")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	on := fs.String("on", "", "profile name (cluster mode). Omit to poll the local store.")
	if err := parseAndCheck(cmdJobsWait, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs wait")
	if err != nil {
		return err
	}
	if *poll <= 0 {
		return fmt.Errorf("jobs wait: --poll must be > 0")
	}

	// fetch returns the run or a permanent/transient error. A nil run +
	// nil err means "not yet visible, keep polling".
	var fetch func() (*store.Run, error)
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return exitError(4, perr)
		}
		if err := requireController(prof, "jobs wait"); err != nil {
			return exitError(4, err)
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		fetch = func() (*store.Run, error) { return c.GetRun(ctx, *runID) }
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return exitError(4, err)
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return exitError(4, oerr)
		}
		defer st.Close()
		fetch = func() (*store.Run, error) { return st.GetRun(ctx, *runID) }
	}

	deadline := time.Now().Add(*timeout)
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()

	// Prime once before the first tick so --timeout 0s-ish paths don't
	// always exit 2 before the first fetch.
	run, ferr := fetch()
	for {
		if ferr != nil {
			// Treat "not found" permanently on the first fetch as a 3;
			// on subsequent fetches, a transient 404 can happen during
			// queue handoff, so we prefer 4 with the raw error.
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
			return exitErrorf(2, "jobs wait: timeout after %s waiting on %s", *timeout, *runID)
		}
		select {
		case <-ctx.Done():
			return exitError(4, ctx.Err())
		case <-ticker.C:
		}
		run, ferr = fetch()
	}
}

// emitWaitResult prints a terse summary of the terminal run so
// `jobs wait` leaves at least one line on stdout scripts can parse.
// JSON emits the full run; table/plain emits one line.
func emitWaitResult(run *store.Run, format string) {
	if run == nil {
		return
	}
	switch format {
	case "json":
		_ = jsonEncode(os.Stdout, run)
	default:
		dur := ""
		if run.FinishedAt != nil {
			dur = run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String()
		}
		fmt.Fprintf(os.Stdout, "%s  %s  %s  %s\n",
			run.ID, run.Pipeline, run.Status, dashIfEmpty(dur))
	}
}

// --- jobs find ---------------------------------------------------

// runJobsFind searches recent runs for a match against git SHA / repo
// / pipeline / since filters. --wait polls until one appears.
func runJobsFind(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsFind.Path, flag.ContinueOnError)
	gitSHA := fs.String("git-sha", "", "match runs whose git SHA starts with this (prefix match)")
	pipeline := fs.String("pipeline", "", "restrict to one pipeline")
	repo := fs.String("repo", "", "match trigger's GITHUB_REPOSITORY env (owner/name)")
	since := fs.Duration("since", time.Hour, "lookback window (default 1h)")
	limit := fs.Int("limit", 20, "max results")
	wait := fs.Bool("wait", false, "block until at least one match appears")
	findTimeout := fs.Duration("find-timeout", 2*time.Minute, "give up after this long when --wait is set")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	quiet := fs.BoolP("quiet", "q", false, "print only run ids, one per line")
	on := fs.String("on", "", "profile name (cluster mode). Omit to search local.")
	if err := parseAndCheck(cmdJobsFind, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *gitSHA == "" && *pipeline == "" && *repo == "" {
		return fmt.Errorf("jobs find: at least one of --git-sha, --pipeline, or --repo is required")
	}
	resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, "jobs find")
	if err != nil {
		return err
	}

	// searchOnce fetches a page of candidate runs and narrows them.
	var searchOnce func() ([]*store.Run, error)
	if *on != "" {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, "jobs find"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		searchOnce = func() ([]*store.Run, error) {
			return findRunsRemote(ctx, c, *gitSHA, *pipeline, *repo, *since, *limit)
		}
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return err
		}
		st, oerr := store.Open(paths.StateDB())
		if oerr != nil {
			return oerr
		}
		defer st.Close()
		searchOnce = func() ([]*store.Run, error) {
			return findRunsLocal(ctx, st, *gitSHA, *pipeline, *repo, *since, *limit)
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
				return exitErrorf(2, "jobs find: timeout after %s with no match", *findTimeout)
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
	return renderFindResults(runs, resolvedFmt, *quiet)
}

// findRunsLocal narrows local runs by the requested filters. Since
// matching on git SHA across the run table is fast (indexed id-order
// scan under a limit), we over-fetch and then filter client-side.
func findRunsLocal(ctx context.Context, st *store.Store, gitSHA, pipeline, repo string,
	since time.Duration, limit int,
) ([]*store.Run, error) {
	filter := store.RunFilter{Limit: limit * 5}
	if pipeline != "" {
		filter.Pipelines = []string{pipeline}
	}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	runs, err := st.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	return narrowRunsByRepo(ctx, runs, gitSHA, repo, limit, func(id string) (map[string]string, error) {
		t, err := st.GetTrigger(ctx, id)
		if err != nil || t == nil {
			return nil, err
		}
		return t.TriggerEnv, nil
	}), nil
}

// findRunsRemote is the controller-side counterpart. Same pattern:
// over-fetch, then filter by SHA/repo client-side. --repo requires a
// per-run trigger lookup, so it's O(N) controller round-trips -- fine
// for the typical N<50 lookback window.
func findRunsRemote(ctx context.Context, c *client.Client, gitSHA, pipeline, repo string,
	since time.Duration, limit int,
) ([]*store.Run, error) {
	filter := store.RunFilter{Limit: limit * 5}
	if pipeline != "" {
		filter.Pipelines = []string{pipeline}
	}
	if since > 0 {
		filter.Since = time.Now().Add(-since)
	}
	runs, err := c.ListRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	return narrowRunsByRepo(ctx, runs, gitSHA, repo, limit, func(id string) (map[string]string, error) {
		t, err := c.GetTrigger(ctx, id)
		if err != nil || t == nil {
			return nil, err
		}
		return t.TriggerEnv, nil
	}), nil
}

// narrowRunsByRepo is the shared SHA+repo filter. SHA is a prefix
// match (short-SHA-friendly); repo is an exact match against the
// GITHUB_REPOSITORY env pulled from the run's trigger row. triggerEnv
// is only fetched when --repo is set, so the SHA-only happy path stays
// one-query.
func narrowRunsByRepo(ctx context.Context, runs []*store.Run, gitSHA, repo string,
	limit int, triggerEnv func(id string) (map[string]string, error),
) []*store.Run {
	_ = ctx
	var out []*store.Run
	for _, r := range runs {
		if gitSHA != "" && !strings.HasPrefix(r.GitSHA, gitSHA) {
			continue
		}
		if repo != "" {
			env, err := triggerEnv(r.ID)
			if err != nil || env == nil {
				continue
			}
			if env["GITHUB_REPOSITORY"] != repo {
				continue
			}
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// renderFindResults emits the search result set in one of three
// shapes: quiet (ids newline-separated, or a JSON array with -o json),
// JSON table (runs array), or a human table.
func renderFindResults(runs []*store.Run, format string, quiet bool) error {
	if quiet {
		if format == "json" {
			ids := make([]string, 0, len(runs))
			for _, r := range runs {
				ids = append(ids, r.ID)
			}
			if ids == nil {
				ids = []string{}
			}
			return jsonEncode(os.Stdout, ids)
		}
		for _, r := range runs {
			fmt.Fprintln(os.Stdout, r.ID)
		}
		return nil
	}
	if format == "json" {
		if runs == nil {
			runs = []*store.Run{}
		}
		return jsonEncode(os.Stdout, runs)
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stdout, "no runs match the requested filter")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tPIPELINE\tSTATUS\tSHA\tSTARTED")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Pipeline, r.Status,
			dashIfEmpty(shortSHAOrDash(r.GitSHA)),
			relTime(r.StartedAt))
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

// --- local helpers -----------------------------------------------

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
