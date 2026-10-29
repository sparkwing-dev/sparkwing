package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func searchRunLogs(ctx context.Context, inv clisurface.Invocation, reader controllerwire.LogReader, out, diagnostics io.Writer) error {
	query, err := parseLogSearch(inv, time.Now())
	if err != nil {
		return err
	}
	page, err := reader.SearchLogs(ctx, query)
	if err != nil {
		return err
	}
	if err := page.Validate(query.Runs.Cursor); err != nil {
		return invalidObservation(err.Error())
	}
	format := runLogOutput(inv)
	if len(page.Matches) == 0 && format == "pretty" && !inv.Bool("quiet") {
		if _, err := fmt.Fprintln(out, "no matches"); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, match := range page.Matches {
		if inv.Bool("quiet") {
			if seen[match.RunID] {
				continue
			}
			seen[match.RunID] = true
			if format == "json" {
				err = json.NewEncoder(out).Encode(struct {
					RunID string `json:"run_id"`
				}{match.RunID})
			} else {
				_, err = fmt.Fprintln(out, match.RunID)
			}
		} else {
			err = writeLogRecord(out, match, format, true)
		}
		if err != nil {
			return err
		}
	}
	return writePageInfo(out, diagnostics, page.NextCursor, page.Truncated, format, inv.Bool("quiet"))
}

func parseLogSearch(inv clisurface.Invocation, now time.Time) (controllerwire.LogSearchQuery, error) {
	runs, err := parseRunQuery(inv, now)
	query := controllerwire.LogSearchQuery{Runs: runs, Job: inv.Value("job"), Pattern: inv.Value("grep"), MaxMatches: 5}
	if err != nil {
		return query, err
	}
	if !inv.Has("limit") {
		query.Runs.Limit = 50
	}
	if inv.Has("max-matches") {
		query.MaxMatches = logFlagInt(inv, "max-matches")
	}
	if query.Pattern == "" || query.MaxMatches < 0 {
		return query, logUsage(inv, "--grep requires a pattern and --max-matches must be nonnegative")
	}
	return query, nil
}
