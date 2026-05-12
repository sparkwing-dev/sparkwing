// `sparkwing runs grep PATTERN` -- substring search across recent
// runs' log bodies. Walks the IMP-048 filter-narrowed candidate set
// and emits one row per matching line. Fills the gap between
// `runs logs --grep` (one known run) and `runs list --error`
// (structured failure reason only) by surfacing free-form log body
// matches the dashboard can't easily answer either.
package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// GrepOpts configures `runs grep`.
type GrepOpts struct {
	Pattern    string
	Limit      int // max runs to scan; 0 -> default 50
	MaxMatches int // per-run match cap; 0 -> no cap
	JSON       bool
	Quiet      bool

	// Filter set built from --pipeline / --status / --branch / etc.
	// Mirrors `runs list` so the candidate run set is the same.
	Pipelines []string
	Statuses  []string
	Since     time.Duration
	Filter    CompiledFilter
}

// GrepMatch is one matching line in the wire shape emitted to JSON.
type GrepMatch struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`
	LineNo int    `json:"line_no"`
	Line   string `json:"line"`
}

const (
	grepDefaultRunLimit = 50
	grepMaxRunLimit     = 1000
)

// resolveRunLimit picks the candidate scan cap. When filters are
// active we over-fetch up to grepMaxRunLimit so the post-filter
// narrowing has room.
func resolveRunLimit(opts GrepOpts) int {
	limit := opts.Limit
	if limit <= 0 {
		limit = grepDefaultRunLimit
	}
	if limit > grepMaxRunLimit {
		limit = grepMaxRunLimit
	}
	return limit
}

// RunGrepLocal scans every node log file for matches.
func RunGrepLocal(ctx context.Context, paths Paths, opts GrepOpts, out io.Writer) error {
	if opts.Pattern == "" {
		return errors.New("runs grep: PATTERN is required")
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	runs, err := st.ListRuns(ctx, store.RunFilter{
		Limit:     grepFetchLimit(opts),
		Pipelines: opts.Pipelines,
		Statuses:  opts.Statuses,
		Since:     sinceCutoff(opts.Since),
	})
	if err != nil {
		return err
	}
	runs = applyClientFilters(runs, opts.Filter)
	if cap := resolveRunLimit(opts); len(runs) > cap {
		runs = runs[:cap]
	}
	matches, err := scanLocalRuns(ctx, st, paths, runs, opts)
	if err != nil {
		return err
	}
	return emitGrepMatches(matches, opts, out)
}

// RunGrepRemote is the cluster-mode counterpart.
func RunGrepRemote(ctx context.Context, controllerURL, logsURL, token string, opts GrepOpts, out io.Writer) error {
	if opts.Pattern == "" {
		return errors.New("runs grep: PATTERN is required")
	}
	if controllerURL == "" || logsURL == "" {
		return errors.New("runs grep: profile must carry both controller and logs URLs")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	logc := sparkwinglogs.New(logsURL, nil, token)
	runs, err := c.ListRuns(ctx, store.RunFilter{
		Limit:     grepFetchLimit(opts),
		Pipelines: opts.Pipelines,
		Statuses:  opts.Statuses,
		Since:     sinceCutoff(opts.Since),
	})
	if err != nil {
		return err
	}
	runs = applyClientFilters(runs, opts.Filter)
	if cap := resolveRunLimit(opts); len(runs) > cap {
		runs = runs[:cap]
	}
	matches, err := scanRemoteRuns(ctx, c, logc, runs, opts)
	if err != nil {
		return err
	}
	return emitGrepMatches(matches, opts, out)
}

// grepFetchLimit over-fetches when any client-side filter is active
// so the candidate set has room to narrow.
func grepFetchLimit(opts GrepOpts) int {
	want := resolveRunLimit(opts)
	if opts.Filter.HasAny() {
		return grepMaxRunLimit
	}
	return want
}

func sinceCutoff(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(-d)
}

// scanLocalRuns walks each run's per-node log file and collects
// substring matches. Stops at MaxMatches per node, not per run, so
// a run with several chatty nodes still surfaces evidence across
// all of them.
func scanLocalRuns(ctx context.Context, st *store.Store, paths Paths, runs []*store.Run, opts GrepOpts) ([]GrepMatch, error) {
	var out []GrepMatch
	for _, r := range runs {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		nodes, err := st.ListNodes(ctx, r.ID)
		if err != nil {
			return out, fmt.Errorf("list nodes for %s: %w", r.ID, err)
		}
		for _, n := range nodes {
			matches, err := grepNodeFile(paths.NodeLog(r.ID, n.NodeID), opts.Pattern, opts.MaxMatches)
			if err != nil {
				return out, err
			}
			for _, m := range matches {
				out = append(out, GrepMatch{RunID: r.ID, NodeID: n.NodeID, LineNo: m.lineNo, Line: m.line})
			}
		}
	}
	return out, nil
}

// scanRemoteRuns delegates each (run, node) substring read to the
// logs service. The service already supports server-side grep, so we
// only see matching bytes back over the wire.
func scanRemoteRuns(ctx context.Context, c *client.Client, logc storage.LogStore, runs []*store.Run, opts GrepOpts) ([]GrepMatch, error) {
	var out []GrepMatch
	for _, r := range runs {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		nodes, err := c.ListNodes(ctx, r.ID)
		if err != nil {
			return out, fmt.Errorf("list nodes for %s: %w", r.ID, err)
		}
		for _, n := range nodes {
			data, err := logc.Read(ctx, r.ID, n.NodeID, storage.ReadOpts{Grep: opts.Pattern})
			if err != nil {
				return out, fmt.Errorf("read %s/%s: %w", r.ID, n.NodeID, err)
			}
			lineNo := 0
			matchCount := 0
			sc := bufio.NewScanner(bytes.NewReader(data))
			sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for sc.Scan() {
				lineNo++
				// The service-side grep guarantees every line matched;
				// still defensively re-check so a future no-op service
				// doesn't quietly turn this into a full-log dump.
				line := sc.Text()
				if !strings.Contains(line, opts.Pattern) {
					continue
				}
				out = append(out, GrepMatch{RunID: r.ID, NodeID: n.NodeID, LineNo: lineNo, Line: line})
				matchCount++
				if opts.MaxMatches > 0 && matchCount >= opts.MaxMatches {
					break
				}
			}
		}
	}
	return out, nil
}

type grepLine struct {
	lineNo int
	line   string
}

// grepNodeFile reads one node log file line by line. Returns at
// most maxMatches entries when >0; otherwise every match.
func grepNodeFile(path, pattern string, maxMatches int) ([]grepLine, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []grepLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if !strings.Contains(line, pattern) {
			continue
		}
		out = append(out, grepLine{lineNo: lineNo, line: line})
		if maxMatches > 0 && len(out) >= maxMatches {
			break
		}
	}
	return out, sc.Err()
}

// emitGrepMatches renders the result set per opts: table (default),
// json (array of matches), or quiet (unique run ids).
func emitGrepMatches(matches []GrepMatch, opts GrepOpts, out io.Writer) error {
	if opts.Quiet {
		seen := map[string]bool{}
		var ids []string
		for _, m := range matches {
			if seen[m.RunID] {
				continue
			}
			seen[m.RunID] = true
			ids = append(ids, m.RunID)
		}
		sort.Strings(ids)
		if opts.JSON {
			if ids == nil {
				ids = []string{}
			}
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(ids)
		}
		for _, id := range ids {
			fmt.Fprintln(out, id)
		}
		return nil
	}
	if opts.JSON {
		if matches == nil {
			matches = []GrepMatch{}
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(matches)
	}
	if len(matches) == 0 {
		fmt.Fprintln(out, "no matches")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tNODE\tLINE\tTEXT")
	for _, m := range matches {
		text := strings.ReplaceAll(m.Line, "\t", "    ")
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", m.RunID, m.NodeID, m.LineNo, text)
	}
	return tw.Flush()
}
