package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type runListOptions struct {
	query                      controllerwire.RunQuery
	format, group, style       string
	quiet, pivot, wait, follow bool
	sparkline                  int
	waitTimeout                time.Duration
}

func parseRunQuery(inv clisurface.Invocation, now time.Time) (controllerwire.RunQuery, error) {
	q := controllerwire.RunQuery{Limit: 20, Cursor: inv.Value("cursor"), Pipeline: inv.Value("pipeline"), Status: inv.Value("status"), SHA: strings.TrimSpace(inv.Value("sha")), Branches: inv.Flags["branch"], Repos: inv.Flags["repo"], RootOnly: inv.Bool("root-only"), Error: inv.Value("error"), Search: inv.Value("search")}
	invalid := func(message string) (controllerwire.RunQuery, error) {
		return q, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	if inv.Has("limit") {
		q.Limit, _ = strconv.Atoi(inv.Value("limit"))
	}
	if q.Limit < 1 {
		return invalid("--limit must be positive")
	}
	if inv.Has("since") {
		d, err := clisurface.ParseLookback(inv.Value("since"))
		if err != nil || d < 0 {
			return invalid("--since must be a nonnegative duration")
		}
		if d > 0 {
			q.StartedAfter = now.Add(-d)
		}
	}
	for _, item := range []struct {
		name string
		dst  *time.Time
	}{{"started-after", &q.StartedAfter}, {"started-before", &q.StartedBefore}, {"finished-after", &q.FinishedAfter}, {"finished-before", &q.FinishedBefore}} {
		if !inv.Has(item.name) {
			continue
		}
		value, err := clisurface.ParseLooseDate(inv.Value(item.name))
		if err != nil {
			return invalid("--" + item.name + " requires a date or lookback duration")
		}
		if item.name != "started-after" || value.After(*item.dst) {
			*item.dst = value
		}
	}
	if !q.StartedBefore.IsZero() && q.StartedAfter.After(q.StartedBefore) || !q.FinishedBefore.IsZero() && q.FinishedAfter.After(q.FinishedBefore) {
		return invalid("date range ends before it starts")
	}
	return q, nil
}

func parseRunList(inv clisurface.Invocation) (runListOptions, error) {
	q, err := parseRunQuery(inv, time.Now())
	o := runListOptions{query: q, format: inv.Output(color.IsInteractiveStdout()), group: inv.Value("group-by"), style: "ascii", quiet: inv.Bool("quiet"), pivot: inv.Bool("by-pipeline"), wait: inv.Bool("wait"), follow: inv.Bool("follow"), sparkline: 30, waitTimeout: 2 * time.Minute}
	if err != nil {
		return o, err
	}
	invalid := func(message string) (runListOptions, error) {
		return o, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	if inv.Has("style") {
		o.style = inv.Value("style")
	}
	if o.style != "ascii" && o.style != "block" && o.style != "dot" {
		return invalid("--style must be ascii|block|dot")
	}
	if inv.Has("sparkline") {
		o.sparkline, _ = strconv.Atoi(inv.Value("sparkline"))
	}
	if o.sparkline < 1 {
		return invalid("--sparkline must be positive")
	}
	if inv.Has("wait-timeout") {
		o.waitTimeout, _ = time.ParseDuration(inv.Value("wait-timeout"))
	}
	if o.waitTimeout <= 0 {
		return invalid("--wait-timeout must be positive")
	}
	if o.group != "" {
		if q.Status != "failed" {
			return invalid("--group-by requires --status failed")
		}
		if o.group != "run" && o.group != "job" && o.group != "step" {
			return invalid("--group-by must be run|job|step")
		}
		if o.pivot {
			return invalid("--group-by and --by-pipeline select different views")
		}
	}
	return o, nil
}

func runRunsList(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if _, err := parseRunList(inv); err != nil {
		return err
	}
	reader, closeTransport, err := resolveRunReader(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	return observeRunList(ctx, inv, reader, os.Stdout, os.Stderr, waitStatusPoll)
}

func observeRunList(ctx context.Context, inv clisurface.Invocation, reader controllerwire.RunReader, out, diagnostics io.Writer, wait func(context.Context, time.Duration) error) error {
	o, err := parseRunList(inv)
	if err != nil {
		return err
	}
	if wait == nil {
		wait = waitStatusPoll
	}
	page, err := firstRunPage(ctx, reader, o, wait)
	if err != nil {
		return err
	}
	if err := renderRunList(ctx, reader, out, diagnostics, page, o); err != nil {
		return err
	}
	if !o.follow {
		return nil
	}
	// safety: mutable filters can match an earlier run after a later run was emitted.
	boundary := maxRunNumber(page.Runs)
	emitted := map[string]bool{}
	for {
		if err := wait(ctx, 2*time.Second); err != nil {
			return exitError(3, err)
		}
		query := o.query
		query.Cursor = ""
		query.AfterNumber = boundary
		var grouped []controllerwire.RunDetail
		err := visitRunPages(ctx, reader, query, func(page controllerwire.RunPage) error {
			var fresh []controllerwire.RunDetail
			for _, run := range page.Runs {
				if run.Number <= query.AfterNumber {
					return invalidObservation("run follow crossed its starting boundary")
				}
				if !emitted[run.ID] {
					fresh = append(fresh, run)
					emitted[run.ID] = true
				}
			}
			page.Runs = fresh
			if o.pivot || o.group != "" {
				grouped = append(grouped, page.Runs...)
				return nil
			}
			if len(page.Runs) == 0 {
				return nil
			}
			return renderRunList(ctx, reader, out, diagnostics, page, o)
		})
		if err != nil {
			return exitError(3, err)
		}
		if len(grouped) > 0 {
			if err := renderRunList(ctx, reader, out, diagnostics, controllerwire.RunPage{Runs: grouped}, o); err != nil {
				return exitError(3, err)
			}
		}
	}
}

func firstRunPage(ctx context.Context, reader controllerwire.RunReader, o runListOptions, wait func(context.Context, time.Duration) error) (controllerwire.RunPage, error) {
	if o.wait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.waitTimeout)
		defer cancel()
	}
	for {
		page, err := reader.ListRuns(ctx, o.query)
		if err != nil {
			if o.wait || o.follow {
				return page, exitError(3, err)
			}
			return page, err
		}
		if err := validateRunPage(page, o.query); err != nil {
			if o.wait || o.follow {
				return page, exitError(3, err)
			}
			return page, err
		}
		if !o.wait || len(page.Runs) > 0 {
			return page, nil
		}
		if err := wait(ctx, 2*time.Second); err != nil {
			return page, exitError(3, fmt.Errorf("waiting for a matching run: %w", err))
		}
	}
}

func validateRunPage(page controllerwire.RunPage, query controllerwire.RunQuery) error {
	if len(page.Runs) > query.Limit {
		return invalidObservation("controller exceeded the requested run page size")
	}
	if page.Truncated && (page.NextCursor == "" || page.NextCursor == query.Cursor) || !page.Truncated && page.NextCursor != "" {
		return invalidObservation("controller returned inconsistent run pagination")
	}
	seen := map[string]bool{}
	var previous int64
	for i, run := range page.Runs {
		if run.ID == "" || run.Status == "" || run.Number <= 0 || seen[run.ID] || i > 0 && run.Number >= previous {
			return invalidObservation("run page has missing, repeated or unordered identities")
		}
		previous = run.Number
		seen[run.ID] = true
	}
	return nil
}

func visitRunPages(ctx context.Context, reader controllerwire.RunReader, query controllerwire.RunQuery, visit func(controllerwire.RunPage) error) error {
	cursors := map[string]bool{query.Cursor: true}
	seen := map[string]bool{}
	var previous int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := reader.ListRuns(ctx, query)
		if err != nil {
			return err
		}
		if err := validateRunPage(page, query); err != nil {
			return err
		}
		for _, run := range page.Runs {
			if seen[run.ID] || previous > 0 && run.Number >= previous {
				return invalidObservation("run pagination repeated or reordered a run")
			}
			seen[run.ID] = true
			previous = run.Number
		}
		if err := visit(page); err != nil {
			return err
		}
		if !page.Truncated {
			return nil
		}
		if cursors[page.NextCursor] {
			return invalidObservation("run pagination repeated a cursor")
		}
		cursors[page.NextCursor] = true
		query.Cursor = page.NextCursor
	}
}

func maxRunNumber(runs []controllerwire.RunDetail) int64 {
	var n int64
	for _, run := range runs {
		if run.Number > n {
			n = run.Number
		}
	}
	return n
}

func writePageInfo(out, diagnostics io.Writer, next string, truncated bool, format string, quiet bool) error {
	if format == "json" {
		if quiet {
			if !truncated {
				return nil
			}
			out = diagnostics
		}
		return json.NewEncoder(out).Encode(struct {
			Kind       string `json:"kind"`
			NextCursor string `json:"next_cursor"`
			Truncated  bool   `json:"truncated"`
		}{"page", next, truncated})
	}
	if truncated {
		_, err := fmt.Fprintf(diagnostics, "more matching runs: --cursor %s\n", next)
		return err
	}
	return nil
}
