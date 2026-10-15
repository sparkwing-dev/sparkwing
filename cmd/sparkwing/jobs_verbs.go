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
	on := fs.String("profile", "", "read against the named profile instead of this machine's store")
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
	if prof != nil {
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
