package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type GrepOpts struct {
	Pattern    string
	Limit      int
	MaxMatches int
	JSON       bool
	Quiet      bool

	Pipelines []string
	Statuses  []string
	Since     time.Duration
	Filter    CompiledFilter
}

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
	defer func() { _ = st.Close() }()
	runs, err := st.ListRuns(ctx, store.RunFilter{
		Limit:          grepFetchLimit(opts),
		Pipelines:      opts.Pipelines,
		Statuses:       opts.Statuses,
		GitBranches:    opts.Filter.Branches,
		GitSHAPrefixes: opts.Filter.SHAPrefixes,
		Since:          sinceCutoff(opts.Since),
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

func RunGrepRemote(ctx context.Context, controllerURL, logsURL, token string, opts GrepOpts, out io.Writer) error {
	if opts.Pattern == "" {
		return errors.New("runs grep: PATTERN is required")
	}
	if controllerURL == "" {
		return errors.New("runs grep: profile must carry a controller URL")
	}
	if logsURL == "" {
		services, err := discovery.ServicesFor(ctx, controllerURL, token)
		if err != nil {
			return fmt.Errorf("runs grep: discover logs service: %w", err)
		}
		if services.Logs == "" {
			return errors.New("runs grep: controller announces no logs service; configure the profile's logs URL")
		}
		logsURL = services.Logs
	}
	c := client.NewWithToken(controllerURL, nil, token)
	logc := logs.NewClientWithToken(logsURL, nil, token).
		WithRunnerIdentity(logs.ProcessIdentity("cli"))
	runs, err := c.ListRuns(ctx, store.RunFilter{
		Limit:          grepFetchLimit(opts),
		Pipelines:      opts.Pipelines,
		Statuses:       opts.Statuses,
		GitBranches:    opts.Filter.Branches,
		GitSHAPrefixes: opts.Filter.SHAPrefixes,
		Since:          sinceCutoff(opts.Since),
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

func scanRemoteRuns(ctx context.Context, c *client.Client, logc *logs.Client, runs []*store.Run, opts GrepOpts) ([]GrepMatch, error) {
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
			matches, err := logc.Grep(ctx, r.ID, n.NodeID, opts.Pattern, opts.MaxMatches)
			if err != nil {
				return out, fmt.Errorf("read %s/%s: %w", r.ID, n.NodeID, err)
			}
			for _, m := range matches {
				out = append(out, GrepMatch{RunID: r.ID, NodeID: n.NodeID, LineNo: m.LineNo, Line: m.Line})
			}
		}
	}
	return out, nil
}

type grepLine struct {
	lineNo int
	line   string
}

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
			return ndjson.Write(out, ids)
		}
		for _, id := range ids {
			fmt.Fprintln(out, id)
		}
		return nil
	}
	if opts.JSON {
		return ndjson.Write(out, matches)
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
