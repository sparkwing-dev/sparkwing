package orchestrator

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
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
	return followLogsRemote(ctx, ctrl, ctrl.NodeLogs(), runID, opts.Node, out)
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

func followLogsRemote(ctx context.Context, ctrl *client.Client, logs storage.AttemptLogStreamer,
	runID, nodeFilter string, out io.Writer,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	f := &attemptFollower{logs: logs, runID: runID, out: out, state: map[logIdentity]followState{}, nodes: map[string]bool{}}
	defer f.wg.Wait()

	wait := remoteFollowPollInterval
	consecutive := 0
	var failures remoteFollowFailures
	for {
		timer := time.NewTimer(wait)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		nodes, err := ctrl.ListNodes(runCtx, runID)
		if client.IsTokenDead(err) {
			return fmt.Errorf("run %s: %w", runID, err)
		}
		run, rerr := ctrl.GetRun(runCtx, runID)
		if rerr == nil {
			failures.succeeded()
			wait, consecutive = remoteFollowPollInterval, 0
			if err == nil && f.poll(runCtx, nodes, nodeFilter, isTerminalStatus(run.Status)) {
				return nil
			}
			continue
		}
		if runCtx.Err() != nil {
			return nil
		}
		// safety: a dead token never reads the run again, so waiting out the budget only adds load.
		if client.IsTokenDead(rerr) {
			return fmt.Errorf("run %s: %w", runID, rerr)
		}
		consecutive++
		wait = remoteFollowBackoff(consecutive)
		if elapsed, exhausted := failures.failed(time.Now(), remoteFollowFailureBudget); exhausted {
			return fmt.Errorf("run %s: controller status unreadable for %s: %w", runID, elapsed.Round(time.Second), rerr)
		}
	}
}

type logIdentity struct {
	node                string
	attempt, generation int64
}

// safety: the node row lists every attempt it ran as well as the one it runs now, so an attempt that started and
// finished between two polls is still followed; an in-process retry writes on under its first attempt's log, so
// some listed attempts never get a log of their own.
func nodeLogIdentities(n *store.Node) []logIdentity {
	var out []logIdentity
	add := func(attempt, generation int64) {
		id := logIdentity{node: n.NodeID, attempt: attempt, generation: generation}
		if attempt > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	for _, a := range n.ExecutionAttempts {
		add(int64(a.Attempt), a.ClaimGeneration)
	}
	if n.Status == "running" {
		add(int64(n.AttemptsConsumed)+1, n.ClaimGeneration)
	}
	return out
}

type followState int

const (
	followDue followState = iota
	followStreaming
	followDone
)

// safety: each attempt's log is streamed by its identity and marked done only once streamed to its end, one
// attempt of a node at a time and in order, so a retry's log is neither skipped nor replaced by another attempt's.
type attemptFollower struct {
	logs  storage.AttemptLogStreamer
	runID string
	out   io.Writer
	wg    sync.WaitGroup

	mu    sync.Mutex
	state map[logIdentity]followState
	nodes map[string]bool
}

// safety: it reports the follow finished only when the run is terminal and every identity it saw is done.
func (f *attemptFollower) poll(ctx context.Context, nodes []*store.Node, nodeFilter string, terminal bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range nodes {
		if nodeFilter != "" && n.NodeID != nodeFilter {
			continue
		}
		for _, id := range nodeLogIdentities(n) {
			if _, seen := f.state[id]; !seen {
				f.state[id], f.nodes[id.node] = followDue, true
			}
		}
	}
	ids := slices.SortedFunc(maps.Keys(f.state), func(a, b logIdentity) int {
		return cmp.Or(strings.Compare(a.node, b.node), cmp.Compare(a.attempt, b.attempt), cmp.Compare(a.generation, b.generation))
	})
	busy := map[string]bool{}
	finished := terminal
	for _, id := range ids {
		st := f.state[id]
		if st == followDue && !busy[id.node] {
			f.state[id], st = followStreaming, followStreaming
			f.wg.Add(1)
			go f.stream(ctx, id, terminal)
		}
		if st == followStreaming {
			busy[id.node] = true
		}
		finished = finished && st == followDone
	}
	return finished
}

// safety: an attempt with no log once the run is terminal never gets one, as when an in-process retry wrote on
// under its first attempt's log, so it is done; before then it is asked for again at the next poll.
func (f *attemptFollower) stream(ctx context.Context, id logIdentity, terminal bool) {
	defer f.wg.Done()
	next := followDue
	body, err := f.logs.StreamAttempt(ctx, f.runID, id.node, id.attempt, id.generation)
	switch {
	case errors.Is(err, storage.ErrNotSupported), terminal && errors.Is(err, storage.ErrNotFound):
		next = followDone
	case err == nil:
		if readSSE(ctx, body, func(line string) { f.print(id.node, line) }) {
			next = followDone
		}
		_ = body.Close()
	}
	f.mu.Lock()
	f.state[id] = next
	f.mu.Unlock()
}

func (f *attemptFollower) print(nodeID, line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.nodes) > 1 {
		fmt.Fprintf(f.out, "%s | %s\n", nodeID, line)
	} else {
		fmt.Fprintln(f.out, line)
	}
}

// safety: stream_end means the node's log is sealed, so the caller stops rather than reconnecting, which would
// replay the whole log; that event's payload is framing, not log text.
func readSSE(ctx context.Context, body io.Reader, onLine func(string)) (ended bool) {
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
		case strings.HasPrefix(line, "data: "):
			onLine(strings.TrimPrefix(line, "data: "))
		case strings.HasPrefix(line, "data:"):
			onLine(strings.TrimPrefix(line, "data:"))
		}
	}
	return false
}
