package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/sparkwinglogs"
)

// ListJobsRemote is the cluster-mode counterpart to ListJobs.
func ListJobsRemote(ctx context.Context, controllerURL, token string, opts ListOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("ListJobsRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	filter := store.RunFilter{
		Limit:     opts.Limit,
		Pipelines: opts.Pipelines,
		Statuses:  opts.Statuses,
	}
	if opts.Since > 0 {
		filter.Since = time.Now().Add(-opts.Since)
	}
	runs, err := c.ListRuns(ctx, filter)
	if err != nil {
		return err
	}
	return renderRunList(runs, opts, out)
}

// JobStatusRemote is the cluster-mode counterpart to JobStatus.
func JobStatusRemote(ctx context.Context, controllerURL, token, runID string, opts StatusOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("JobStatusRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)

	render := func() error {
		run, err := c.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		nodes, err := c.ListNodes(ctx, runID)
		if err != nil {
			return err
		}
		if opts.JSON {
			return writeJSON(out, map[string]any{"run": run, "nodes": nodes})
		}
		return renderRemoteStatus(run, nodes, out, opts.Follow)
	}

	if !opts.Follow {
		return render()
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	first := true
	for {
		if !first {
			fmt.Fprint(out, "\033[H\033[J")
		}
		first = false
		if err := render(); err != nil {
			return err
		}
		run, err := c.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if isTerminalStatus(run.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func renderRemoteStatus(run *store.Run, nodes []*store.Node, out io.Writer, followBanner bool) error {
	if followBanner {
		fmt.Fprintf(out, "# following %s (ctrl-c to stop)\n\n", run.ID)
	}
	fmt.Fprintf(out, "run:       %s\n", run.ID)
	fmt.Fprintf(out, "pipeline:  %s\n", run.Pipeline)
	fmt.Fprintf(out, "status:    %s\n", run.Status)
	fmt.Fprintf(out, "trigger:   %s\n", orDash(run.TriggerSource))
	fmt.Fprintf(out, "started:   %s  (%s)\n",
		run.StartedAt.Local().Format("2006-01-02 15:04:05"),
		relativeAge(run.StartedAt),
	)
	if run.FinishedAt != nil {
		fmt.Fprintf(out, "finished:  %s  (duration %s)\n",
			run.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond))
	} else {
		fmt.Fprintf(out, "elapsed:   %s\n", time.Since(run.StartedAt).Round(100*time.Millisecond))
	}
	if run.Error != "" {
		fmt.Fprintf(out, "error:     %s\n", run.Error)
	}
	if run.GitBranch != "" || run.GitSHA != "" {
		fmt.Fprintf(out, "git:       %s @ %s\n", run.GitBranch, shortSHA(run.GitSHA))
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "nodes (%d total, %d done):\n", len(nodes), countFinished(nodes))
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  ID\tSTATUS\tOUTCOME\tDURATION\tDEPS")
	for _, n := range nodes {
		outcome := n.Outcome
		if outcome == "" {
			outcome = "-"
		}
		deps := strings.Join(n.Deps, ",")
		if deps == "" {
			deps = "-"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			n.NodeID, n.Status, outcome, formatNodeDuration(n), deps)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, n := range nodes {
		if n.Error != "" && n.Error != "upstream-failed" {
			fmt.Fprintf(out, "\n%s error:\n  %s\n", n.NodeID, indent(n.Error, "  "))
		}
	}
	return nil
}

// JobErrorsRemote is the cluster-mode counterpart to JobErrors.
func JobErrorsRemote(ctx context.Context, controllerURL, token, runID string, asJSON bool, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("JobErrorsRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	type failedNode struct {
		Node    string `json:"node"`
		Outcome string `json:"outcome"`
		Error   string `json:"error"`
	}
	var failed []failedNode
	for _, n := range nodes {
		if n.Outcome == "failed" && n.Error != "" {
			failed = append(failed, failedNode{Node: n.NodeID, Outcome: n.Outcome, Error: n.Error})
		}
	}
	if asJSON {
		return writeJSON(out, failed)
	}
	if len(failed) == 0 {
		fmt.Fprintln(out, "no failing nodes")
		return nil
	}
	for _, f := range failed {
		fmt.Fprintf(out, "%s:\n  %s\n\n", f.Node, indent(f.Error, "  "))
	}
	return nil
}

// GetRunJSONRemote fetches a run + nodes and writes pretty JSON.
func GetRunJSONRemote(ctx context.Context, controllerURL, token, runID string, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("GetRunJSONRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"run": run, "nodes": nodes})
}

// GetRunJSONLocal is the local counterpart of GetRunJSONRemote.
// Factored here rather than in jobs_cli.go so the two live side-by-
// side; they're the backing pair for `sparkwing jobs get`.
func GetRunJSONLocal(ctx context.Context, paths Paths, runID string, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	return writeRunDetailJSON(ctx, st, runID, out)
}

// JobLogsRemote is the cluster-mode counterpart to JobLogs.
func JobLogsRemote(ctx context.Context, controllerURL, logsURL, runID string, opts LogsOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("JobLogsRemote: controller URL required")
	}
	if logsURL == "" {
		return errors.New("JobLogsRemote: logs URL required")
	}
	if opts.Tree {
		return errors.New("JobLogsRemote: --tree is local-mode only")
	}
	return JobLogsRemoteWithTokens(ctx, controllerURL, logsURL, "", runID, opts, out)
}

// JobLogsRemoteWithTokens adds a bearer token to JobLogsRemote.
func JobLogsRemoteWithTokens(ctx context.Context, controllerURL, logsURL, token, runID string, opts LogsOpts, out io.Writer) error {
	if opts.EventsOnly {
		// IMP-010 ships envelope-event persistence + reader for local
		// mode only; the remote logs service ingests per-node body
		// output today, not run-level envelope events. Filing as a
		// follow-up; failing loudly here is better than silently
		// returning an empty stream.
		return errors.New("--events-only is local-mode only today (remote envelope ingestion is a follow-up; see CHANGELOG IMP-010)")
	}
	ctrl := client.NewWithToken(controllerURL, nil, token)
	var logc storage.LogStore = sparkwinglogs.New(logsURL, nil, token)

	// Follow discovers nodes over time so ExpandFrom children appear.
	if !opts.Follow {
		nodes, err := ctrl.ListNodes(ctx, runID)
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}
		target, err := filterTarget(nodes, opts.Node, runID)
		if err != nil {
			return err
		}
		target = filterNodesBySince(target, opts.Since)
		return writeLogsTextRemote(ctx, logc, runID, target, opts, out)
	}
	return followLogsRemote(ctx, ctrl, logc, runID, opts.Node, out)
}

func filterTarget(nodes []*store.Node, want, runID string) ([]*store.Node, error) {
	if want == "" {
		return nodes, nil
	}
	for _, n := range nodes {
		if n.NodeID == want {
			return []*store.Node{n}, nil
		}
	}
	return nil, fmt.Errorf("node %q not found in run %s", want, runID)
}

func writeLogsTextRemote(ctx context.Context, logc storage.LogStore, runID string, target []*store.Node, opts LogsOpts, out io.Writer) error {
	filter := storage.ReadOpts{
		Tail:  opts.Tail,
		Head:  opts.Head,
		Lines: opts.Lines,
		Grep:  opts.Grep,
	}
	// JSON = flat JSONL; no banners.
	jsonOut := opts.JSON || opts.Format == "json"
	for i, n := range target {
		if len(target) > 1 && !jsonOut {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "=== %s (%s) ===\n", n.NodeID, orDash(n.Outcome))
		}
		if n.StartedAt == nil {
			if len(target) > 1 && !jsonOut {
				fmt.Fprintln(out, "(did not execute)")
			}
			continue
		}
		data, err := logc.Read(ctx, runID, n.NodeID, filter)
		if err != nil {
			return fmt.Errorf("read %s: %w", n.NodeID, err)
		}
		if len(data) > 0 && data[0] == '{' {
			if err := renderJSONLStream(bytes.NewReader(data), opts, out); err != nil {
				return err
			}
			continue
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// followLogsRemote tails live logs by polling ListNodes and spawning
// per-node SSE goroutines. Exits when run is terminal (with a short
// drain) or ctx cancels.
func followLogsRemote(ctx context.Context, ctrl *client.Client, logc storage.LogStore,
	runID, nodeFilter string, out io.Writer,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One writer mutex so interleaved node lines don't corrupt output.
	var writeMu sync.Mutex
	seen := map[string]struct{}{}
	var wg sync.WaitGroup
	// multi flips on the second-node discovery; promotes earlier
	// single-node output to prefixed mode retroactively.
	var multi atomic.Bool

	spawn := func(nodeID string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			streamNode(runCtx, logc, runID, nodeID, &multi, &writeMu, out)
		}()
	}

	terminal := make(chan struct{})

	go func() {
		defer close(terminal)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				nodes, err := ctrl.ListNodes(runCtx, runID)
				if err == nil {
					for _, n := range nodes {
						if nodeFilter != "" && n.NodeID != nodeFilter {
							continue
						}
						if _, ok := seen[n.NodeID]; ok {
							continue
						}
						seen[n.NodeID] = struct{}{}
						if len(seen) > 1 {
							multi.Store(true)
						}
						spawn(n.NodeID)
					}
				}
				run, err := ctrl.GetRun(runCtx, runID)
				if err == nil && isTerminalStatus(run.Status) {
					return
				}
			}
		}
	}()

	<-terminal
	// Drain window so streams flush final lines.
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
	}
	cancel()
	wg.Wait()
	return nil
}

// streamNode reads one node's SSE stream, reconnecting on errors.
func streamNode(ctx context.Context, logc storage.LogStore, runID, nodeID string,
	multi *atomic.Bool, mu *sync.Mutex, out io.Writer,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		body, err := logc.Stream(ctx, runID, nodeID)
		if err != nil {
			// File may not exist yet; back off.
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}
		readSSE(ctx, body, func(line string) {
			mu.Lock()
			defer mu.Unlock()
			if multi != nil && multi.Load() {
				fmt.Fprintf(out, "%s | %s\n", nodeID, line)
			} else {
				fmt.Fprintln(out, line)
			}
		})
		body.Close()
	}
}

// readSSE invokes onLine for each "data: ..." payload until EOF/ctx.
func readSSE(ctx context.Context, body io.Reader, onLine func(string)) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Text()
		switch {
		case line == "":
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data: "):
			onLine(strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, "data:"):
			onLine(strings.TrimPrefix(line, "data:"))
		}
	}
}
