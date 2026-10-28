package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
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
	runs, err := client.NewRunReader(options)
	if err != nil {
		return exitError(3, err)
	}
	return observeRunLogs(ctx, inv, reader, runs, os.Stdout, os.Stderr, waitStatusPoll)
}

func followControllerRunLogs(ctx context.Context, options client.EngineOptions, run, format string, out io.Writer) error {
	inv := clisurface.Invocation{Command: clisurface.Command{Path: "sparkwing runs logs"}, Flags: map[string][]string{"run": {run}, "follow": {"true"}, "output": {format}}}
	reader, err := client.NewLogReader(options)
	if err != nil {
		return err
	}
	return observeRunLogs(ctx, inv, reader, nil, out, io.Discard, waitStatusPoll)
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

func observeRunLogs(ctx context.Context, inv clisurface.Invocation, reader controllerwire.LogReader, runs controllerwire.RunReader, out, diagnostics io.Writer, wait func(context.Context, time.Duration) error) error {
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
			return writeUnloggedFailures(ctx, inv, runs, query.Job, out)
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

func writeUnloggedFailures(ctx context.Context, inv clisurface.Invocation, runs controllerwire.RunReader, job string, out io.Writer) error {
	if runs == nil {
		return nil
	}
	detail, err := runs.ReadRun(ctx, inv.Value("run"))
	if err != nil {
		return exitError(3, err)
	}
	if detail.Failure != "" && job == "" {
		if runLogOutput(inv) == "json" {
			err = json.NewEncoder(out).Encode(struct {
				controllerwire.LogRecord
				Hint string `json:"hint,omitempty"`
			}{controllerwire.LogRecord{RunID: detail.ID, Event: "run_error", Line: detail.Failure}, planFailureHint(detail)})
		} else {
			var text strings.Builder
			writeRunError(&text, detail)
			_, err = io.WriteString(out, text.String())
		}
		if err != nil {
			return err
		}
	}
	started := map[string]bool{}
	for _, attempt := range detail.Attempts {
		started[attempt.JobID] = true
	}
	for _, failed := range detail.Jobs {
		if failed.Error == "" || started[failed.ID] || job != "" && failed.ID != job {
			continue
		}
		if runLogOutput(inv) == "json" {
			err = json.NewEncoder(out).Encode(controllerwire.LogRecord{RunID: detail.ID, Event: "job_failed_before_start", JobID: failed.ID, Line: failed.Error})
		} else {
			_, err = fmt.Fprintf(out, "%s failed before it started, so it has no logs; its error:\n%s\n", failed.ID, strings.TrimRight(failed.Error, "\n"))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeLogRecord(out io.Writer, record controllerwire.LogRecord, format string, search bool) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	line := record.Line
	if record.Event != "" {
		line = eventDetail(record.Line)
	}
	if format == "plain" && !search {
		if record.Event != "" {
			line = eventText(record, line)
		}
		_, err := fmt.Fprintln(out, line)
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
	if record.Event != "" && line == "" {
		_, err := fmt.Fprintf(out, "%s/%s:%d\n", record.RunID, subject, record.LineNo)
		return err
	}
	_, err := fmt.Fprintf(out, "%s/%s:%d: %s\n", record.RunID, subject, record.LineNo, line)
	return err
}

func eventText(record controllerwire.LogRecord, detail string) string {
	words := strings.ReplaceAll(record.Event, "_", " ")
	if rest, ok := strings.CutPrefix(words, "job "); ok && record.JobID != "" {
		words = "job " + record.JobID + " " + rest
	}
	if detail == "" {
		return words
	}
	return words + ": " + detail
}

// safety: event payloads are JSON objects; plain and pretty output show their values,
// and anything that is not an object stays verbatim so no detail is lost.
func eventDetail(payload string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &fields) != nil || fields == nil {
		if payload == "null" {
			return ""
		}
		return payload
	}
	if reason, ok := fields["reason"]; ok && len(fields) == 1 {
		return eventValue(reason)
	}
	keys := slices.Sorted(maps.Keys(fields))
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+eventValue(fields[key]))
	}
	return strings.Join(parts, " ")
}

func eventValue(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
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
