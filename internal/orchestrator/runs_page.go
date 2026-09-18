package orchestrator

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunsPage is the trailing record every `runs list` page carries, so a caller can tell
// a full result from a cut one without knowing the server's ceiling.
type RunsPage struct {
	Kind       string `json:"kind"`
	Total      *int   `json:"total,omitempty"`
	Returned   int    `json:"returned"`
	Limit      int    `json:"limit"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type runsCursor struct {
	StartedAt int64
	ID        string
	// safety: the window's lower bound travels with the cursor. Re-deriving it from the
	// clock moves it into rows a newest-first walk has yet to reach, dropping them unread.
	Since int64
}

func (c runsCursor) encode() string {
	return strconv.FormatInt(c.StartedAt, 10) + ":" + c.ID + ":" + strconv.FormatInt(c.Since, 10)
}

func parseRunsCursor(raw string) (runsCursor, error) {
	malformed := fmt.Errorf("--cursor %q is not a cursor this listing produced; repeat the query without one", raw)
	// hack: a run id may carry a colon, so the numeric fields are taken from the ends.
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

func newRunsPager(limit int, clientFilter CompiledFilter) runsPager {
	page := effectiveRunsPageLimit(limit)
	return runsPager{limit: page, budget: listFetchLimitForFilter(page, clientFilter) + 1}
}

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
	pageSize := opts.Limit
	if opts.ByPipeline {
		pageSize = store.MaxRunListLimit
	}
	pager := newRunsPager(pageSize, clientFilter)
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

func runStartedAtKey(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return startedAt.UnixNano()
}
