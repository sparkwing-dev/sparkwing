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
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ListJobsRemote(ctx context.Context, controllerURL, token string, opts ListOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("ListJobsRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	filter := store.RunFilter{
		Limit:     listFetchLimit(opts),
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
	runs = applyClientFilters(runs, opts.Filter)
	if opts.ByPipeline {
		opts.Pivot.JSON = opts.JSON
		opts.Pivot.Quiet = opts.Quiet
		return RenderPipelinePivot(runs, opts.Pivot, out)
	}
	if opts.Limit > 0 && len(runs) > opts.Limit {
		runs = runs[:opts.Limit]
	}
	return renderRunList(runs, opts, out, nil)
}

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
		steps, _ := c.ListNodeSteps(ctx, runID)
		approvals, _ := c.ListApprovalsForRun(ctx, runID)
		if opts.JSON {
			wrapped := withFailureExcerpts(joinStepsByNode(nodes, steps),
				failureExcerptsFor(ctx, c, runID, failedNodeIDs(nodes)))
			payload := map[string]any{"run": store.RedactedRun(run), "nodes": wrapped}
			if len(approvals) > 0 {
				payload["approvals"] = approvals
			}
			return writeJSON(out, payload)
		}
		return renderRemoteStatus(run, nodes, groupStepsByNode(steps), approvals, out, opts.Follow, opts.Steps)
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

func renderRemoteStatus(run *store.Run, nodes []*store.Node, stepsByNode map[string][]*store.NodeStep, approvals []*store.Approval, out io.Writer, followBanner, includeSteps bool) error {
	if followBanner {
		fmt.Fprintf(out, "# following %s (ctrl-c to stop)\n\n", run.ID)
	}
	fmt.Fprintf(out, "run:       %s\n", run.ID)
	fmt.Fprintf(out, "pipeline:  %s\n", run.Pipeline)
	fmt.Fprintf(out, "status:    %s\n", run.Status)
	fmt.Fprintf(out, "trigger:   %s\n", orDash(run.TriggerSource))
	fmt.Fprintf(
		out, "started:   %s  (%s)\n",
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
	renderNodesWithSteps(out, nodes, stepsByNode, includeSteps)
	for _, n := range nodes {
		if n.Error != "" && n.Error != "upstream-failed" {
			fmt.Fprintf(out, "\n%s error:\n  %s\n", n.NodeID, indent(n.Error, "  "))
		}
	}
	renderApprovalsSection(out, approvals)
	return nil
}

func RemoteRunOutcome(ctx context.Context, controllerURL, token, runID string, out io.Writer) (string, error) {
	if controllerURL == "" {
		return "", errors.New("RemoteRunOutcome: controller URL required")
	}
	if out == nil {
		out = io.Discard
	}
	c := client.NewWithToken(controllerURL, nil, token)
	run, err := getRunRetrying(ctx, c, runID)
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", fmt.Errorf("run %s: controller returned no run record", runID)
	}
	if run.Status == "success" {
		return run.Status, nil
	}
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		fmt.Fprintf(out, "\nrun %s finished %s (node detail unavailable: %v)\n", run.ID, run.Status, err)
		return run.Status, nil
	}
	steps, _ := c.ListNodeSteps(ctx, runID)
	approvals, _ := c.ListApprovalsForRun(ctx, runID)
	fmt.Fprintln(out)
	_ = renderRemoteStatus(run, nodes, groupStepsByNode(steps), approvals, out, false, false)
	return run.Status, nil
}

const remoteOutcomeRetryDelay = 250 * time.Millisecond

func getRunRetrying(ctx context.Context, c *client.Client, runID string) (*store.Run, error) {
	run, err := c.GetRun(ctx, runID)
	if err == nil {
		return run, nil
	}
	select {
	case <-ctx.Done():
		return nil, err
	case <-time.After(remoteOutcomeRetryDelay):
	}
	return c.GetRun(ctx, runID)
}

func JobErrorsRemote(ctx context.Context, controllerURL, token, runID string, asJSON bool, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("JobErrorsRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		return err
	}

	var excerpts failureExcerptIndex
	if asJSON {
		excerpts = failureExcerptsFor(ctx, c, runID, failedNodeIDs(nodes))
	}
	failed := failedNodeReports(nodes, excerpts)
	if asJSON {
		return writeNDJSON(out, failed)
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
	return writeJSON(out, map[string]any{"run": store.RedactedRun(run), "nodes": nodes})
}

func GetRunJSONLocal(ctx context.Context, paths Paths, runID string, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return writeRunDetailJSON(ctx, st, runID, out)
}

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

func JobLogsRemoteWithTokens(ctx context.Context, controllerURL, logsURL, token, runID string, opts LogsOpts, out io.Writer) error {
	if opts.EventsOnly {
		return errors.New("--events-only is local-mode only today (remote envelope ingestion is a follow-up)")
	}
	ctrl := client.NewWithToken(controllerURL, nil, token)
	var logc storage.LogStore = sparkwinglogs.New(logsURL, nil, token)

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

var (
	remoteFollowFailureBudget = 60 * time.Second
	remoteFollowPollInterval  = 300 * time.Millisecond
)

type remoteFollowFailures struct {
	since time.Time
}

func (f *remoteFollowFailures) succeeded() {
	f.since = time.Time{}
}

func (f *remoteFollowFailures) failed(now time.Time, budget time.Duration) (time.Duration, bool) {
	if f.since.IsZero() {
		f.since = now
		return 0, false
	}
	elapsed := now.Sub(f.since)
	return elapsed, elapsed >= budget
}

func followLogsRemote(ctx context.Context, ctrl *client.Client, logc storage.LogStore,
	runID, nodeFilter string, out io.Writer,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var writeMu sync.Mutex
	seen := map[string]struct{}{}
	var wg sync.WaitGroup
	var multi atomic.Bool

	spawn := func(nodeID string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			streamNode(runCtx, logc, runID, nodeID, &multi, &writeMu, out)
		}()
	}

	terminal := make(chan struct{})

	var followErr error

	go func() {
		defer close(terminal)
		ticker := time.NewTicker(remoteFollowPollInterval)
		defer ticker.Stop()

		var failures remoteFollowFailures
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
				if err == nil {
					failures.succeeded()
					if isTerminalStatus(run.Status) {
						return
					}
					continue
				}

				if runCtx.Err() != nil {
					return
				}
				elapsed, exhausted := failures.failed(time.Now(), remoteFollowFailureBudget)
				if exhausted {
					followErr = fmt.Errorf("run %s: controller status unreadable for %s: %w",
						runID, elapsed.Round(time.Second), err)
					return
				}
			}
		}
	}()

	<-terminal
	if len(seen) > 0 {
		select {
		case <-time.After(600 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	cancel()
	wg.Wait()
	return followErr
}

func streamNode(ctx context.Context, logc storage.LogStore, runID, nodeID string,
	multi *atomic.Bool, mu *sync.Mutex, out io.Writer,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		body, err := logc.Stream(ctx, runID, nodeID)
		if err != nil {
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
