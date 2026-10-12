package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	st, label, done, err := OpenStoreForRun(ctx, paths, runID)
	if err != nil {
		return err
	}
	defer done()
	return writeRunDetailJSON(ctx, st, runID, label, out)
}

func JobLogsRemoteWithTokens(ctx context.Context, controllerURL, token, runID string, opts LogsOpts, out io.Writer) error {
	ctrl := client.NewWithToken(controllerURL, nil, token)
	if opts.EventsOnly {
		return writeEventsViaBackend(ctx, backend.NewClientBackend(ctrl, nil), runID, opts, out)
	}
	var logc storage.LogStore = ctrl.NodeLogs()

	b := backend.NewClientBackend(ctrl, logc)
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
		return writeLogsViaBackend(ctx, b, runID, target, opts, out)
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

var (
	remoteFollowFailureBudget = 60 * time.Second
	remoteFollowPollInterval  = 300 * time.Millisecond
	remoteFollowBackoffCap    = 5 * time.Second
)

func remoteFollowBackoff(n int) time.Duration {
	return min(remoteFollowPollInterval<<min(n, 16), max(remoteFollowBackoffCap, remoteFollowPollInterval))
}

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

// safety: once the run is terminal a node whose stream never carried a line may never have run, so the follow
// waits this long for it and no longer.
var followDrain = 600 * time.Millisecond

// safety: the controller follows each node across its attempts in this run and ends its stream once the node is
// done, so the follow keeps one stream per node, resumes it from the last event id after a transport failure, and
// ends with an error on a refusal no retry gets past.
func followLogsRemote(ctx context.Context, ctrl *client.Client, logc storage.LogStore,
	runID, nodeFilter string, out io.Writer,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	var writeMu sync.Mutex
	var multi atomic.Bool
	streams := map[string]*followedNode{}
	refused := make(chan error, 1)
	segmentsChecked := false
	var terminalAt time.Time

	wait := remoteFollowPollInterval
	consecutive := 0
	var failures remoteFollowFailures
	for {
		timer := time.NewTimer(wait)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return nil
		case err := <-refused:
			timer.Stop()
			return fmt.Errorf("run %s: %w", runID, err)
		case <-timer.C:
		}
		nodes, err := ctrl.ListNodes(runCtx, runID)
		if client.IsTokenDead(err) {
			return fmt.Errorf("run %s: %w", runID, err)
		}
		for _, n := range nodes {
			if (nodeFilter != "" && n.NodeID != nodeFilter) || streams[n.NodeID] != nil {
				continue
			}
			if !segmentsChecked {
				keeps, err := ctrl.KeepsLogSegments(runCtx)
				if err != nil {
					return fmt.Errorf("run %s: %w", runID, err)
				}
				if !keeps {
					return fmt.Errorf("run %s: the controller keeps no node log segments, so it has no node log stream "+
						"to follow; it predates them or has no store to keep them in", runID)
				}
				segmentsChecked = true
			}
			f := &followedNode{done: make(chan struct{})}
			streams[n.NodeID] = f
			multi.Store(len(streams) > 1)
			wg.Add(1)
			go func(nodeID string) {
				defer wg.Done()
				defer close(f.done)
				if err := f.follow(runCtx, logc, runID, nodeID, func(line string) {
					writeMu.Lock()
					defer writeMu.Unlock()
					if multi.Load() {
						fmt.Fprintf(out, "%s | %s\n", nodeID, line)
					} else {
						fmt.Fprintln(out, line)
					}
				}); err != nil && runCtx.Err() == nil {
					select {
					case refused <- fmt.Errorf("node %s: %w", nodeID, err):
					default:
					}
				}
			}(n.NodeID)
		}
		run, err := ctrl.GetRun(runCtx, runID)
		if err == nil {
			failures.succeeded()
			wait, consecutive = remoteFollowPollInterval, 0
			if isTerminalStatus(run.Status) {
				if terminalAt.IsZero() {
					terminalAt = time.Now()
				}
				if followFinished(streams, time.Since(terminalAt) >= followDrain) {
					return nil
				}
			}
			continue
		}
		if runCtx.Err() != nil {
			return nil
		}
		// safety: a dead token never reads the run again, so waiting out the budget only adds load.
		if client.IsTokenDead(err) {
			return fmt.Errorf("run %s: %w", runID, err)
		}
		consecutive++
		wait = remoteFollowBackoff(consecutive)
		if elapsed, exhausted := failures.failed(time.Now(), remoteFollowFailureBudget); exhausted {
			return fmt.Errorf("run %s: controller status unreadable for %s: %w", runID, elapsed.Round(time.Second), err)
		}
	}
}

type followedNode struct {
	started atomic.Bool
	done    chan struct{}
}

// safety: a node whose stream ended is finished; one whose stream never carried a line may belong to a node that
// never ran, so once the run is terminal it waits only out the drain.
func followFinished(streams map[string]*followedNode, drained bool) bool {
	for _, f := range streams {
		select {
		case <-f.done:
		default:
			if f.started.Load() || !drained {
				return false
			}
		}
	}
	return true
}

func (f *followedNode) follow(ctx context.Context, logc storage.LogStore, runID, nodeID string, onLine func(string)) error {
	cursor := ""
	failures := 0
	for ctx.Err() == nil {
		body, err := logc.Stream(storage.WithLogCursor(ctx, cursor), runID, nodeID)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, client.ErrLogStreamRefused):
			return err
		case err == nil && body == nil:
			return nil
		case err != nil:
			failures++
		default:
			failures = 0
			ended := readSSE(ctx, body, func(id string) {
				cursor = id
				f.started.Store(true)
			}, func(line string) {
				f.started.Store(true)
				onLine(line)
			})
			_ = body.Close()
			if ended {
				return nil
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(remoteFollowBackoff(failures)):
		}
	}
	return nil
}

// safety: stream_end means the node is done and its last attempt sealed, so the caller stops rather than
// reconnecting; that event's payload is framing, not log text, and onID hears each event id to resume from.
func readSSE(ctx context.Context, body io.Reader, onID, onLine func(string)) (ended bool) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return false
		}
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			if strings.TrimSpace(strings.TrimPrefix(line, "event:")) == "stream_end" {
				return true
			}
		case strings.HasPrefix(line, "id:"):
			if onID != nil {
				onID(strings.TrimSpace(strings.TrimPrefix(line, "id:")))
			}
		case strings.HasPrefix(line, "data: "):
			onLine(strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, "data:"):
			onLine(strings.TrimPrefix(line, "data:"))
		}
	}
	return false
}
