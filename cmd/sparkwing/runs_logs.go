package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runRunsLogs(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, runLogOutput(inv))
	}
	if _, err := parseRunLogs(inv); err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return exitError(3, err)
	}
	defer closeTransport()
	reader, err := client.NewLogReader(options)
	if err != nil {
		return exitError(3, err)
	}
	return observeRunLogs(ctx, inv, reader, os.Stdout, os.Stderr, waitStatusPoll)
}

func followControllerRunLogs(ctx context.Context, options client.EngineOptions, run, format string, out io.Writer) error {
	inv := clisurface.Invocation{Command: clisurface.Command{Path: "sparkwing runs logs"}, Flags: map[string][]string{"run": {run}, "follow": {"true"}, "output": {format}}}
	reader, err := client.NewLogReader(options)
	if err != nil {
		return err
	}
	return observeRunLogs(ctx, inv, reader, out, io.Discard, waitStatusPoll)
}

func parseRunLogs(inv clisurface.Invocation) (controllerwire.LogQuery, error) {
	q := controllerwire.LogQuery{Job: inv.Value("job"), Lines: inv.Value("lines"), Grep: inv.Value("grep"), Cursor: inv.Value("cursor"), Tree: inv.Bool("tree"), EventsOnly: inv.Bool("events-only"), NoEvents: inv.Bool("no-events")}
	if inv.Has("tail") {
		v := logFlagInt(inv, "tail")
		q.Tail = &v
	}
	if inv.Has("head") {
		v := logFlagInt(inv, "head")
		q.Head = &v
	}
	if inv.Has("since") {
		q.Since, _ = clisurface.ParseLookback(inv.Value("since"))
	}
	if err := q.Validate(); err != nil {
		return q, logUsage(inv, err.Error())
	}
	if inv.Value("run") == "" {
		if _, err := parseLogSearch(inv, time.Now()); err != nil {
			return q, err
		}
		for _, name := range []string{"tail", "head", "lines", "tree", "events-only", "no-events", "follow"} {
			if inv.Has(name) {
				return q, logUsage(inv, "--"+name+" requires --run")
			}
		}
	} else {
		for _, name := range []string{"pipeline", "status", "branch", "sha", "started-after", "started-before", "limit", "max-matches", "quiet"} {
			if inv.Has(name) {
				return q, logUsage(inv, "--"+name+" searches across runs; pass --grep without --run")
			}
		}
	}
	return q, nil
}

func observeRunLogs(ctx context.Context, inv clisurface.Invocation, reader controllerwire.LogReader, out, diagnostics io.Writer, wait func(context.Context, time.Duration) error) error {
	query, err := parseRunLogs(inv)
	if err != nil {
		return err
	}
	if inv.Value("run") == "" {
		return searchRunLogs(ctx, inv, reader, out, diagnostics)
	}
	if wait == nil {
		wait = waitStatusPoll
	}
	seen := map[string]bool{query.Cursor: true}
	for {
		page, err := reader.ReadLogs(ctx, inv.Value("run"), query)
		if err != nil {
			return exitError(3, err)
		}
		if err := page.Validate(query.Cursor); err != nil {
			return exitError(3, invalidObservation(err.Error()))
		}
		if page.NextCursor != "" && page.NextCursor != query.Cursor && seen[page.NextCursor] {
			return exitError(3, invalidObservation("controller repeated an earlier log cursor"))
		}
		for _, record := range page.Records {
			if err := writeLogRecord(out, record, runLogOutput(inv), false); err != nil {
				return err
			}
		}
		if !page.Truncated && (!inv.Bool("follow") || page.Complete) {
			return nil
		}
		query.Cursor = page.NextCursor
		seen[query.Cursor] = true
		if !page.Truncated {
			if err := wait(ctx, time.Second); err != nil {
				return exitError(3, fmt.Errorf("log observation incomplete: %w", err))
			}
		}
	}
}

func writeLogRecord(out io.Writer, record controllerwire.LogRecord, format string, search bool) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	if format == "plain" && !search {
		_, err := fmt.Fprintln(out, record.Line)
		return err
	}
	subject := record.Event
	if record.JobID != "" {
		subject = record.JobID
		if record.StepID != "" {
			subject += "/" + record.StepID
		}
		subject += " " + record.Event
	}
	if record.Subject != nil {
		if attempt := record.Subject.Attempt; attempt != nil {
			subject = attempt.NodeID + "#" + strconv.FormatInt(attempt.Attempt, 10)
		} else if unit := record.Subject.Unit; unit != nil {
			subject = string(unit.Kind)
		}
	}
	_, err := fmt.Fprintf(out, "%s/%s:%d: %s\n", record.RunID, subject, record.LineNo, record.Line)
	return err
}

func logFlagInt(inv clisurface.Invocation, name string) int {
	n, _ := strconv.Atoi(inv.Value(name))
	return n
}

func logUsage(inv clisurface.Invocation, message string) error {
	return &clisurface.UsageError{Path: inv.Command.Path, Message: message}
}

func runLogOutput(inv clisurface.Invocation) string {
	return inv.Output(color.IsInteractiveStdout())
}
