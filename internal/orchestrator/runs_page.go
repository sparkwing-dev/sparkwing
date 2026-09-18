package orchestrator

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunsPage is the trailing record every `runs list` page carries, so a caller
// reading one page can tell a full result from a cut one without knowing what
// the server's ceiling is.
type RunsPage struct {
	Kind       string `json:"kind"`
	Total      *int   `json:"total,omitempty"`
	Returned   int    `json:"returned"`
	Limit      int    `json:"limit"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// safety: both halves travel together, because runs sharing an instant would
// otherwise be skipped as a group.
type runsCursor struct {
	StartedAt int64
	ID        string
	// safety: the window's lower bound travels with the cursor so a walk keeps the one
	// it began with. Re-deriving it from the clock moves the bound toward the rows a
	// newest-first walk has yet to reach, dropping them unread.
	Since int64
}

func (c runsCursor) encode() string {
	return strconv.FormatInt(c.StartedAt, 10) + ":" + c.ID + ":" + strconv.FormatInt(c.Since, 10)
}

func parseRunsCursor(raw string) (runsCursor, error) {
	malformed := fmt.Errorf("--cursor %q is not a cursor this listing produced; repeat the query without one", raw)
	// hack: a run id may carry a colon, so the two numeric fields are taken from the
	// ends and the id is whatever lies between them.
	instant, rest, found := strings.Cut(strings.TrimSpace(raw), ":")
	if !found {
		return runsCursor{}, malformed
	}
	end := strings.LastIndex(rest, ":")
	if end < 0 {
		return runsCursor{}, malformed
	}
	id, sinceText := rest[:end], rest[end+1:]
	if id == "" {
		return runsCursor{}, malformed
	}
	startedAt, err := strconv.ParseInt(instant, 10, 64)
	if err != nil {
		return runsCursor{}, malformed
	}
	since, err := strconv.ParseInt(sinceText, 10, 64)
	if err != nil {
		return runsCursor{}, malformed
	}
	return runsCursor{StartedAt: startedAt, ID: id, Since: since}, nil
}

func cursorFor(r *store.Run, since time.Time) string {
	if r == nil {
		return ""
	}
	c := runsCursor{StartedAt: runStartedAtKey(r.StartedAt), ID: r.ID}
	if !since.IsZero() {
		c.Since = since.UnixNano()
	}
	return c.encode()
}

func (p RunsPage) write(jsonOut bool, stdout, stderr io.Writer) error {
	if jsonOut {
		return writeNDJSON(stdout, []RunsPage{p})
	}
	if !p.Truncated {
		return nil
	}
	if p.Total != nil {
		_, err := fmt.Fprintf(stderr,
			"showing %d of %d runs; continue with the same filters and --cursor %s\n",
			p.Returned, *p.Total, p.NextCursor)
		return err
	}
	_, err := fmt.Fprintf(stderr,
		"showing %d runs and more remain; continue with the same filters and --cursor %s\n",
		p.Returned, p.NextCursor)
	return err
}

type runsPager struct {
	limit  int
	budget int
}

// safety: the window runs at least one row wider than the page, which is what
// tells a full page from a cut one without counting the table.
func newRunsPager(limit int, clientFilter CompiledFilter) runsPager {
	page := effectiveRunsPageLimit(limit)
	return runsPager{limit: page, budget: listFetchLimitForFilter(page, clientFilter) + 1}
}

// safety: the resume point is the last run examined rather than the last one kept,
// so a window the client filters empty still advances.
func (p runsPager) window(raw []TaggedRun, clientFilter CompiledFilter) (
	rows []TaggedRun, resume *store.Run, more bool,
) {
	more = len(raw) >= p.budget
	if len(raw) > p.budget-1 {
		raw = raw[:p.budget-1]
	}
	resume = lastRun(raw)
	return applyClientFiltersTagged(raw, clientFilter), resume, more
}

func (p runsPager) page(rows []TaggedRun, resume *store.Run, more bool, since time.Time) ([]TaggedRun, RunsPage) {
	out := RunsPage{Kind: "page", Limit: p.limit}
	if len(rows) > p.limit {
		rows = rows[:p.limit]
		out.Truncated, out.NextCursor = true, cursorFor(lastRun(rows), since)
	} else if more {
		out.Truncated, out.NextCursor = true, cursorFor(resume, since)
	}
	out.Returned = len(rows)
	return rows, out
}

func runsQueryFor(opts ListOpts) (store.RunFilter, CompiledFilter, runsPager, error) {
	clientFilter := opts.Filter
	clientFilter.Branches = nil
	clientFilter.SHAPrefixes = nil
	// perf: a rollup reads every page and shows none of them, so it pages at the
	// ceiling. --limit sizes what a listing displays, and a small one here would only
	// multiply round trips.
	pageSize := opts.Limit
	if opts.ByPipeline {
		pageSize = store.MaxRunListLimit
	}
	pager := newRunsPager(pageSize, clientFilter)
	// safety: a rollup's page size is this command's own, so a server that cannot serve
	// the probe row reports a short rollup rather than refusing a number the caller
	// never chose.
	probeChosenHere := opts.ByPipeline

	filter := store.RunFilter{
		Limit:             pager.budget,
		ProbeMayBeClamped: probeChosenHere,
		Pipelines:         opts.Pipelines,
		Statuses:          opts.Statuses,
		GitBranches:       opts.Filter.Branches,
		GitSHAPrefixes:    opts.Filter.SHAPrefixes,
	}
	if opts.Since > 0 {
		filter.Since = time.Now().Add(-opts.Since)
	}
	if opts.Cursor != "" {
		cursor, err := parseRunsCursor(opts.Cursor)
		if err != nil {
			return store.RunFilter{}, CompiledFilter{}, runsPager{}, err
		}
		filter.AfterStartedAt, filter.AfterID = cursor.StartedAt, cursor.ID
		if cursor.Since != 0 {
			filter.Since = time.Unix(0, cursor.Since)
		}
	}
	return filter, clientFilter, pager, nil
}

// safety: a run that never started reads back as the zero time, whose nanosecond
// value addresses no stored row. It is carried as zero, which the store reads as
// the whole never-started group.
func runStartedAtKey(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return startedAt.UnixNano()
}
