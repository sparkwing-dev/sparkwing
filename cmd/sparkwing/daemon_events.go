package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type daemonEventQuery struct {
	Run         string
	Kinds       []string
	Incarnation uint64
	Since       time.Duration
}

func runDaemonEvents(inv clisurface.Invocation) error {
	return runDaemonEventsCommand(inv, os.Stdout, os.Stderr, time.Now(), func() (string, []journal.Record, int, error) {
		dir, err := wingd.StateDir("")
		if err != nil {
			return "", nil, 0, err
		}
		records, skipped, err := journal.ReadWithStats(dir)
		return dir, records, skipped, err
	})
}

func runDaemonEventsCommand(inv clisurface.Invocation, out, diagnostics io.Writer, now time.Time, read func() (string, []journal.Record, int, error)) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	query, limit, err := parseDaemonEventQuery(inv)
	if err != nil {
		return err
	}
	cursor, err := decodeDaemonEventCursor(inv.Value("cursor"))
	if err != nil {
		return err
	}
	if cursor.Version != 0 && (cursor.Cutoff.IsZero() != (query.Since == 0)) {
		return invalidDaemonEventCursor()
	}
	dir, records, skipped, err := read()
	if err != nil {
		return err
	}
	if skipped > 0 {
		if _, err := fmt.Fprintf(diagnostics, "Skipped %d unreadable journal records.\n", skipped); err != nil {
			return err
		}
	}
	if inv.Bool("explain") {
		return explainRunAdmission(out, records, query.Run, format)
	}
	page, err := selectDaemonEventPage(records, dir, query, limit, cursor, now)
	if err != nil {
		return err
	}
	if len(records) == 0 && format != "json" {
		_, err := fmt.Fprintf(out, "No events are retained in %s.\n", dir)
		return err
	}
	for _, record := range page.records {
		if format == "json" {
			err = json.NewEncoder(out).Encode(record)
		} else {
			_, err = fmt.Fprintf(out, "%s  #%d.%d  %-22s %s %s\n", record.TS.Format(time.RFC3339Nano), record.Incarnation, record.Seq, record.Kind, record.RunID, compactData(record.Data))
		}
		if err != nil {
			return err
		}
	}
	if format == "json" {
		return writePageInfo(out, diagnostics, page.next, page.next != "", format, false)
	}
	if page.next != "" {
		_, err = fmt.Fprintf(diagnostics, "Older matching events: repeat the same filters with --cursor %s\n", page.next)
	}
	return err
}

func parseDaemonEventQuery(inv clisurface.Invocation) (daemonEventQuery, int, error) {
	invalid := func(message string) error { return &clisurface.UsageError{Path: inv.Command.Path, Message: message} }
	query := daemonEventQuery{Run: inv.Value("run"), Kinds: slices.Clone(inv.Flags["kind"])}
	slices.Sort(query.Kinds)
	query.Kinds = slices.Compact(query.Kinds)
	limit := 50
	if inv.Has("limit") {
		number, err := strconv.Atoi(inv.Value("limit"))
		if err != nil || number < 1 {
			return query, 0, invalid("--limit must be a positive integer")
		}
		limit = number
	}
	if inv.Has("incarnation") {
		number, err := strconv.ParseUint(inv.Value("incarnation"), 10, 64)
		if err != nil {
			return query, 0, invalid("--incarnation must be nonnegative")
		}
		query.Incarnation = number
	}
	if inv.Has("since") {
		duration, err := clisurface.ParseLookback(inv.Value("since"))
		if err != nil || duration < 0 {
			return query, 0, invalid("--since must be a nonnegative duration")
		}
		query.Since = duration
	}
	return query, limit, nil
}

func staleDaemonEventCursor() error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "retained event history changed during pagination", Hint: "restart daemon events without --cursor"}
}
