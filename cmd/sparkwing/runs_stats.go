package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type pipelineStats struct {
	Pipeline   string        `json:"pipeline"`
	Runs       int           `json:"runs"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Cancelled  int           `json:"cancelled"`
	Running    int           `json:"running"`
	SuccessPct float64       `json:"success_pct"`
	AvgDur     time.Duration `json:"avg_duration_ns"`
	P95Dur     time.Duration `json:"p95_duration_ns"`
}

type runStatisticsOptions struct {
	query              controllerwire.RunQuery
	format             string
	sizing, reset, all bool
}

func parseRunStatistics(inv clisurface.Invocation, now time.Time) (runStatisticsOptions, error) {
	q, err := parseRunQuery(inv, now)
	o := runStatisticsOptions{query: q, format: inv.Output(color.IsInteractiveStdout()), sizing: inv.Bool("sizing"), reset: inv.Bool("reset"), all: inv.Bool("all-pipelines")}
	o.query.RootOnly = true
	if err != nil {
		return o, err
	}
	invalid := func(message string) (runStatisticsOptions, error) {
		return o, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	if o.reset {
		if (q.Pipeline != "") == o.all {
			return invalid("--reset requires --pipeline or --all-pipelines, exclusively")
		}
		if o.all && !inv.Bool("yes") {
			return invalid("--reset --all-pipelines requires --yes")
		}
	} else if o.all || inv.Has("yes") {
		return invalid("--all-pipelines and --yes require --reset")
	}
	if inv.Has("since") && (o.sizing || o.reset) {
		return invalid("--since selects run statistics, not sizing or reset")
	}
	return o, nil
}

func runJobsStats(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	o, err := parseRunStatistics(inv, time.Now())
	if err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	if o.sizing || o.reset {
		manager, err := client.NewSizingManager(options)
		if err != nil {
			return err
		}
		return observeSizing(ctx, inv, manager, os.Stdout)
	}
	reader, err := client.NewRunReader(options)
	if err != nil {
		return err
	}
	return observeRunStatistics(ctx, inv, reader, os.Stdout, time.Now())
}

func observeRunStatistics(ctx context.Context, inv clisurface.Invocation, reader controllerwire.RunReader, out io.Writer, now time.Time) error {
	o, err := parseRunStatistics(inv, now)
	if err != nil {
		return err
	}
	groups := map[string][]controllerwire.RunDetail{}
	err = visitRunPages(ctx, reader, o.query, func(page controllerwire.RunPage) error {
		for _, run := range page.Runs {
			if run.Inputs.Pipeline == "" {
				return invalidObservation("run statistics require a pipeline identity")
			}
			groups[run.Inputs.Pipeline] = append(groups[run.Inputs.Pipeline], run)
		}
		return nil
	})
	if err != nil {
		return err
	}
	stats := make([]pipelineStats, 0, len(groups))
	for pipeline, runs := range groups {
		row, err := aggregateRunDetails(pipeline, runs)
		if err != nil {
			return err
		}
		stats = append(stats, row)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Pipeline < stats[j].Pipeline })
	return writeRunStatistics(out, stats, o.format)
}

func aggregateRunDetails(name string, runs []controllerwire.RunDetail) (pipelineStats, error) {
	s := pipelineStats{Pipeline: name, Runs: len(runs)}
	var durations []time.Duration
	for _, run := range runs {
		switch run.Status {
		case "success":
			s.Passed++
		case "failed":
			s.Failed++
		case "cancelled":
			s.Cancelled++
		default:
			s.Running++
		}
		if run.FinishedAt == nil || run.StartedAt == nil {
			continue
		}
		if run.FinishedAt.Before(*run.StartedAt) {
			return s, invalidObservation("run finished before it started")
		}
		d := run.FinishedAt.Sub(*run.StartedAt)
		durations = append(durations, d)
	}
	if n := s.Passed + s.Failed + s.Cancelled; n > 0 {
		s.SuccessPct = float64(s.Passed) / float64(n) * 100
	}
	if len(durations) > 0 {
		// safety: divide before summing so long histories cannot overflow duration.
		n := time.Duration(len(durations))
		var remainder time.Duration
		for _, d := range durations {
			s.AvgDur += d / n
			remainder += d % n
			s.AvgDur += remainder / n
			remainder %= n
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		s.P95Dur = durations[int(math.Ceil(float64(len(durations))*0.95))-1]
	}
	return s, nil
}

func writeRunStatistics(out io.Writer, stats []pipelineStats, format string) error {
	if format == "json" {
		for _, stat := range stats {
			if err := json.NewEncoder(out).Encode(stat); err != nil {
				return err
			}
		}
		return nil
	}
	if format == "plain" {
		for _, stat := range stats {
			if _, err := fmt.Fprintln(out, stat.Pipeline); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tRUNS\tPASS\tFAIL\tCANCEL\tACTIVE\tSUCCESS\tAVG\tP95")
	for _, s := range stats {
		success := "-"
		if s.Passed+s.Failed+s.Cancelled > 0 {
			success = fmt.Sprintf("%.0f%%", s.SuccessPct)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", s.Pipeline, s.Runs, s.Passed, s.Failed, s.Cancelled, s.Running, success, fmtDur(s.AvgDur), fmtDur(s.P95Dur))
	}
	return tw.Flush()
}
