// Package local runs each node of a local run as its own operating
// system process.
//
// The orchestrator IS the compiled pipeline binary, so the spawn
// target is the running executable: a node process re-enters the same
// program at `run-node --coordinated` and executes exactly the body
// the dispatcher would otherwise have run on one of its own
// goroutines. Same binary means the kernel shares its text pages, so
// the per-node cost is a fork and a plan rebuild rather than a second
// copy of the program.
//
// What the child does NOT redo is coordination. The dispatcher
// resolved the cache lookup, the concurrency slot, and the SkipIf
// predicates before it decided to spawn anything -- a cache hit spawns
// no process at all -- so the child is told to skip them.
//
// The child reaches the run's state through a controller the
// dispatcher mounted on loopback, not by opening the SQLite file
// directly: N processes writing one SQLite file is the wedge this
// repository already carries a guard subsystem for, and the
// controller path is the one a pod uses too.
//
// One process per node is also what makes a node restartable on its
// own. An operator can bounce a live node -- `sparkwing runs bounce`
// -- and this runner kills that process and runs the node again
// without the node ever reaching a terminal state, so the run it
// belongs to carries on and nothing downstream sees a failure.
package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ParentLivenessFD is the descriptor a node process reads to learn
// that its dispatcher is gone. The dispatcher holds the write end for
// the life of the run and never writes to it, so the read end reports
// EOF exactly when the dispatcher's process dies -- including the kill
// -9 that leaves no signal to catch. Descriptor 3 is the first entry
// of exec.Cmd.ExtraFiles.
const ParentLivenessFD = 3

// ParentLivenessFDEnv names the descriptor above, and its presence is
// what makes the descriptor claimable.
//
// A number alone is not evidence of ownership: descriptor 3 is
// whatever the surrounding process opened, and `go test` for one
// hands its binary a pipe there. Probing it and finding a pipe would
// therefore be a false positive, after which the watcher would read
// and close a descriptor belonging to something else. Only a
// dispatcher that actually passed the pipe sets this.
const ParentLivenessFDEnv = "SPARKWING_PARENT_LIVENESS_FD"

// superviseInterval is how often the loop that watches a live node
// touches its heartbeat and asks whether an operator has requested a
// bounce. It matches the cluster runner's heartbeat cadence: a missed
// heartbeat is a dashboard annoyance, not a correctness problem, and a
// bounce is an operator action whose worst case is waiting one tick
// for the kill.
const superviseInterval = 5 * time.Second

// bouncePollTimeout bounds the bounce poll so a slow controller cannot
// delay the heartbeat that shares its loop.
//
// The heartbeat is not cosmetic here: a node whose heartbeat goes
// stale past the reconciler's threshold is reaped as abandoned, and
// that fails the whole run. The poll runs on the same goroutine, so
// without its own deadline it would inherit the client's 30-second
// timeout and could push the gap between two heartbeats past that
// threshold -- an operator convenience taking down the run it was
// meant to save. Two seconds is generous for a loopback read and small
// enough to be invisible against the interval.
const bouncePollTimeout = 2 * time.Second

// bounceRetryDelay spaces the retries of the two bounce writes that
// must not be lost: the read that decides whether a killed node is
// re-run, and the consume that closes the request.
const bounceRetryDelay = 200 * time.Millisecond

const (
	// bounceReadAttempts is how many times the post-kill node read is
	// tried before the runner refuses to guess.
	bounceReadAttempts = 3
	// bounceConsumeAttempts is how many times closing a request is tried.
	bounceConsumeAttempts = 3
	// bounceSweepLimit bounds the end-of-node sweep. Each pass closes one
	// request, and a node with more open requests than this has an
	// operator holding down a key, not a bug to spin on.
	bounceSweepLimit = 8
)

// Config is what a dispatcher tells the runner about the run it is
// spawning nodes for.
type Config struct {
	// Executable is the binary every node process re-enters. Resolved
	// once at startup and held: os.Executable() answers from the
	// process's own image, and under `go run` that image is a temporary
	// file whose path must be captured before anything can clean it up.
	Executable string

	// ControllerURL is the loopback controller the child reads run
	// state through. AgentToken authenticates to it.
	ControllerURL string
	AgentToken    string

	// WorkDir is the directory every node process starts in. Nodes
	// resolve their workspace, their project config, and their profile
	// relative to it, so it must be the run's own working directory
	// rather than wherever the dispatcher happened to be invoked.
	WorkDir string

	// Home is SPARKWING_HOME for the child.
	Home string

	// CacheURL is the operator's explicit SPARKWING_CACHE_URL, pinned
	// into the child's environment. It must come from the dispatcher's
	// own environment rather than from $SPARKWING_HOME/dev.env, whose
	// value belongs to a resident dashboard and not to this run. The
	// child prefers its profile's cache surface over this and falls
	// back to it only when the profile declares none.
	CacheURL string

	// LeaseTokens reports the dispatcher's admission lease for the node
	// about to run, so a node process attaches to the lease its run
	// already holds instead of opening a second one and charging the
	// host twice. It is a callback because the lease is acquired after
	// the runner is built, and is nil when no daemon admitted the run.
	LeaseTokens func(context.Context) (lease, child string)

	// DryRun, StartAt, and StopAt are run-level selections the child
	// cannot see any other way; see the SPARKWING_* names in childEnv.
	DryRun  bool
	StartAt string
	StopAt  string

	// TerminationGrace is how long a cancelled node's process tree gets
	// after SIGTERM before SIGKILL. Zero uses procgroup's default.
	TerminationGrace time.Duration

	// SuperviseInterval is the cadence of the loop that heartbeats a
	// live node and polls for a bounce request. Zero uses
	// superviseInterval; tests shorten it so a bounce lands in
	// milliseconds rather than in the operator-facing five seconds.
	SuperviseInterval time.Duration

	// Labels are advertised for WhenRunner matching.
	Labels []string

	Logger *slog.Logger
}

// Runner spawns one process per node.
type Runner struct {
	cfg  Config
	ctrl *client.Client
}

// New builds a Runner. ctrl is the loopback controller client the
// runner reads terminal node rows and writes heartbeats through.
func New(ctrl *client.Client, cfg Config) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.TerminationGrace <= 0 {
		cfg.TerminationGrace = procgroup.DefaultTerminationGrace
	}
	if cfg.SuperviseInterval <= 0 {
		cfg.SuperviseInterval = superviseInterval
	}
	if len(cfg.Labels) == 0 {
		cfg.Labels = []string{"local"}
	}
	return &Runner{cfg: cfg, ctrl: ctrl}
}

var (
	_ runner.Runner          = (*Runner)(nil)
	_ runner.LabelAdvertiser = (*Runner)(nil)
)

// SetLeaseTokenSource installs [Config.LeaseTokens] after
// construction, which is when the dispatcher can supply it: the
// admission lease is acquired inside the run, after the runner exists.
func (r *Runner) SetLeaseTokenSource(fn func(context.Context) (string, string)) {
	r.cfg.LeaseTokens = fn
}

// AdvertisedLabels implements runner.LabelAdvertiser.
func (r *Runner) AdvertisedLabels() []string {
	out := make([]string, len(r.cfg.Labels))
	copy(out, r.cfg.Labels)
	return out
}

// RunNode spawns the node's process and supervises it to a terminal
// outcome, re-running it in place for each operator bounce that
// arrives while it is live.
//
// A bounce is the one way a node process dies without the node dying
// with it. Nothing terminal is written between the kill and the
// respawn, so the dispatcher's waiters see no outcome and downstream
// nodes cannot cascade-fail on a node that is, from their side, still
// running. The heartbeat spans the gap for the same reason: a node
// that stopped heartbeating mid-bounce would be reaped as abandoned.
//
// One request is one kill and one respawn. Nothing here loops on its
// own -- an operator who bounces a wedged node repeatedly is the loop,
// and their intent is a row each time.
//
// No request outlives the node it named. Whatever ends the node -- its
// own exit, cancellation, a spawn that failed -- every request still
// open for it is settled before this returns, because a request left
// pending is not inert: the next dispatch of the same node id (an
// auto-retry) would find it on its first poll and kill an attempt
// nobody bounced.
func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	bounces := make(chan *store.NodeBounce, 1)
	superviseCtx, stopSupervise := context.WithCancel(ctx)
	superviseDone := make(chan struct{})
	go func() { defer close(superviseDone); r.supervise(superviseCtx, req, bounces) }()
	ledger := bounceLedger{}
	defer func() {
		// safety: the order is load-bearing. Stopping the poll and waiting for
		// it to exit is what makes the sweep final -- a poll still
		// running could hand over a request after the sweep looked.
		stopSupervise()
		<-superviseDone
		r.settleOpenBounces(context.WithoutCancel(ctx), req, ledger)
	}()

	// safety: the machine paid for every attempt, including the ones an
	// operator killed, so what the node cost is their sum -- the same
	// accumulation an auto-retry's attempts get.
	var total *runner.ResourceUsage
	for {
		res, bounced := r.runAttempt(ctx, req, bounces, ledger)
		total = addUsage(total, res.Usage)
		if !bounced {
			res.Usage = total
			return res
		}
	}
}

// pollBounce asks for this node's oldest open request under its own
// short deadline, so a controller that stops answering costs the
// supervision loop one bouncePollTimeout rather than the client's full
// timeout -- which the heartbeat sharing this loop would pay for.
func (r *Runner) pollBounce(ctx context.Context, req runner.Request) (*store.NodeBounce, error) {
	pollCtx, cancel := context.WithTimeout(ctx, bouncePollTimeout)
	defer cancel()
	return r.ctrl.PendingNodeBounce(pollCtx, req.RunID, req.NodeID)
}

// bounceLedger records what this RunNode decided about each request it
// took, keyed by seq.
//
// It exists because deciding and recording are two steps with a
// network between them: a consume that fails leaves a row still
// pending while the runner has already acted on it. The sweep at the
// end of RunNode consults this so a request that was honored is
// finally recorded as honored, rather than being relabelled a miss by
// the cleanup that had to finish the job.
//
// One RunNode owns one node, and every write happens on the goroutine
// running the attempts, so it needs no lock.
type bounceLedger map[int64]string

// runAttempt runs the node's process once. It reports the attempt's
// result and whether an operator bounce ended it, in which case the
// result carries only what the killed process cost: no outcome was
// written and the node is still running as far as everything outside
// this runner is concerned.
func (r *Runner) runAttempt(ctx context.Context, req runner.Request, bounces <-chan *store.NodeBounce, ledger bounceLedger) (runner.Result, bool) {
	cmd := exec.Command(r.cfg.Executable, nodeArgv(r.cfg.ControllerURL, req.RunID, req.NodeID)...)
	cmd.Dir = r.cfg.WorkDir
	cmd.Env = childEnv(ctx, os.Environ(), r.cfg, req)

	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return failedResult(fmt.Errorf("local runner: stdout pipe for %s: %w", req.NodeID, err)), false
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdout, stdoutW)
		return failedResult(fmt.Errorf("local runner: stderr pipe for %s: %w", req.NodeID, err)), false
	}
	livenessR, livenessW, err := os.Pipe()
	if err != nil {
		closeAll(stdout, stdoutW, stderr, stderrW)
		return failedResult(fmt.Errorf("local runner: liveness pipe for %s: %w", req.NodeID, err)), false
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.ExtraFiles = []*os.File{livenessR}

	// safety: the dispatcher holds only the write end. Its close -- deliberate
	// here, or by the kernel when the dispatcher dies -- is the EOF the
	// child watches for.
	defer func() { _ = livenessW.Close() }()

	spawnedAt := time.Now()
	group, err := procgroup.Start(cmd)
	// safety: the child owns its copies now; a parent that keeps the write ends
	// open never sees EOF on its own readers.
	closeAll(stdoutW, stderrW, livenessR)
	if err != nil {
		closeAll(stdout, stderr)
		return failedResult(fmt.Errorf("local runner: spawn %s: %w", req.NodeID, err)), false
	}

	var forwarders sync.WaitGroup
	forwarders.Add(2)
	go func() { defer forwarders.Done(); forwardRecords(stdout, req, r.cfg.Logger) }()
	go func() { defer forwarders.Done(); forwardStderr(stderr, req) }()

	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID,
		fmt.Sprintf("running, pid %d", group.ID()))

	ending, bounce, waitErr := r.await(ctx, group, bounces)
	wall := time.Since(spawnedAt)
	forwarders.Wait()
	closeAll(stdout, stderr)

	usage := usageFrom(cmd.ProcessState)
	if usage != nil {
		usage.Wall = wall
	}
	cancelled := ending == endCancelled
	if ending == endBounced {
		switch r.settleBounce(context.WithoutCancel(ctx), req, bounce, ledger, ctx.Err() != nil) {
		case bounceRespawn:
			return runner.Result{Usage: usage}, true
		case bounceTornDown:
			// safety: the run is going away, so the kill this bounce asked for
			// is indistinguishable from teardown's own. Classifying it as
			// cancellation leaves the row to teardown, which is the only
			// writer that knows whether the run was cancelled or superseded.
			cancelled = true
		case bounceMissed:
			// safety: the node wrote its terminal row before the kill landed;
			// resultFor reads that row and it wins, exactly as it would
			// have without a bounce in flight.
		}
	}

	return r.resultFor(ctx, req, cmd, waitErr, cancelled, usage), false
}

// nodeArgv is the child's command line. Flags precede the positionals
// because run-node's flag.Parse stops at the first non-flag argument.
//
// --logs is passed explicitly empty: left off, it would default to the
// logs URL in $SPARKWING_HOME/dev.env and route this run's node logs
// into a resident dashboard's logs service instead of the run's own
// log files. --controller is passed rather than left to the same
// dev.env fallback, so the child talks to THIS run's controller even
// on a machine where a dashboard has announced another.
func nodeArgv(controllerURL, runID, nodeID string) []string {
	return []string{
		"run-node",
		"--coordinated",
		"--controller=" + controllerURL,
		"--logs=",
		runID,
		nodeID,
	}
}

// attemptEnding is what ended one attempt's process.
type attemptEnding int

const (
	// endExited: the process ended on its own terms.
	endExited attemptEnding = iota
	// endCancelled: the run was cancelled and this runner terminated
	// the process tree.
	endCancelled
	// endBounced: an operator asked for the node to be restarted and
	// this runner terminated the process tree to do it.
	endBounced
)

// await waits for the process tree, terminating it when the run is
// cancelled or an operator bounces the node. It reports the wait
// error, what ended the process, and -- for a bounce -- the request
// that did.
//
// A bounce and a cancellation kill identically: SIGTERM to the group,
// the configured grace, then SIGKILL, with the tree proven empty
// before the attempt is over. They differ only in what is written
// afterwards, which is the caller's decision.
func (r *Runner) await(ctx context.Context, group *procgroup.Group, bounces <-chan *store.NodeBounce) (attemptEnding, *store.NodeBounce, error) {
	done := make(chan error, 1)
	go func() { done <- group.Finish(context.WithoutCancel(ctx), r.cfg.TerminationGrace) }()

	var ending attemptEnding
	var bounce *store.NodeBounce
	select {
	case err := <-done:
		return endExited, nil, err
	case <-ctx.Done():
		ending = endCancelled
	case b := <-bounces:
		ending, bounce = endBounced, b
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*r.cfg.TerminationGrace+30*time.Second)
	defer cancel()
	if err := group.Terminate(termCtx, r.cfg.TerminationGrace); err != nil && !errors.Is(err, procgroup.ErrCleanup) {
		r.cfg.Logger.Debug("local runner: terminate", "err", err)
	}
	return ending, bounce, <-done
}

// bounceVerdict is what a bounced attempt does next.
type bounceVerdict int

const (
	// bounceRespawn: the kill landed on live work; run the node again.
	bounceRespawn bounceVerdict = iota
	// bounceMissed: the node reached its terminal row before the kill
	// landed. The row wins -- a finished node has nothing to restart --
	// and the request is closed as missed.
	bounceMissed
	// bounceTornDown: the run itself is ending, so there is nothing to
	// restart the node for.
	bounceTornDown
)

// settleBounce records how one bounce request ended and reports what
// the attempt should do next.
//
// The node row decides, which is the cluster runner's rule: the row
// the executing process wrote is the truth, and a dead executor
// without one is a failure unless something explicitly intended
// otherwise. This request is that explicit intent, so it is consumed
// either way -- a request must not outlive the kill it asked for, or
// the next poll would act on it a second time.
//
// A read that will not answer is not a verdict. The node's state after
// the kill decides whether the node is re-run at all, so the read is
// retried, and if it still will not answer the node is NOT re-run:
// respawning blind risks executing the node's steps a second time over
// a node that had already finished, and the ordinary exit path below
// resolves the attempt with whatever it can read instead.
func (r *Runner) settleBounce(ctx context.Context, req runner.Request, b *store.NodeBounce, ledger bounceLedger, tearingDown bool) bounceVerdict {
	if b == nil {
		return bounceMissed
	}
	verdict, outcome, kind, reason := bounceRespawn, store.BounceBounced, "node_bounced", ""
	switch {
	case tearingDown:
		verdict, outcome, kind = bounceTornDown, store.BounceMissed, "node_bounce_missed"
		reason = "the run was ending"
	default:
		n, err := r.nodeAfterKill(ctx, req)
		switch {
		case err != nil:
			verdict, outcome, kind = bounceMissed, store.BounceMissed, "node_bounce_missed"
			reason = fmt.Sprintf("the node's state could not be read after the kill (%v), so it was not re-run", err)
			r.cfg.Logger.Warn("local runner: read node after bounce kill",
				"run_id", req.RunID, "node_id", req.NodeID, "err", err)
		case runner.NodeTerminal(n):
			verdict, outcome, kind = bounceMissed, store.BounceMissed, "node_bounce_missed"
			reason = fmt.Sprintf("the node finished (%s) before the kill landed", n.Outcome)
		}
	}

	attrs := map[string]any{
		"seq":          b.Seq,
		"requested_by": b.RequestedBy,
		"requested_at": b.RequestedAt,
		// safety: the node keeps the admission lease its run already holds --
		// the work was admitted once, and an operator restarting it is
		// not a re-price. Recorded on the event so a capacity question
		// asked later is answered by the run's own history.
		"admission_lease_retained": true,
	}
	if reason != "" {
		attrs["reason"] = reason
	}
	payload, _ := json.Marshal(attrs)
	if err := r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID, kind, payload); err != nil {
		r.cfg.Logger.Warn("local runner: record bounce event",
			"run_id", req.RunID, "node_id", req.NodeID, "kind", kind, "err", err)
	}
	// safety: recorded before the consume, not after. A consume that fails
	// leaves the row pending while this runner has already acted on it,
	// and the sweep at the end of RunNode has to close that row with the
	// verdict actually reached rather than relabelling it a miss.
	ledger[b.Seq] = outcome
	// safety: a consume that fails after every retry is logged there and left
	// to the sweep at the end of RunNode, which closes it with the
	// verdict the ledger just recorded. The attempt does not stall on it:
	// the node's own progress is not the request's bookkeeping.
	_ = r.consumeBounce(ctx, req, b.Seq, outcome)
	return verdict
}

// nodeAfterKill reads the node row a killed attempt left behind,
// retrying a read that fails.
//
// The read is the only evidence distinguishing "the operator's kill
// landed on live work" from "the node had already finished", and both
// branches are consequential -- one re-runs the node's steps, the
// other does not. One dropped loopback request is not a reason to
// choose either blind.
func (r *Runner) nodeAfterKill(ctx context.Context, req runner.Request) (*store.Node, error) {
	var err error
	for attempt := range bounceReadAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(bounceRetryDelay):
			}
		}
		var n *store.Node
		n, err = r.ctrl.GetNode(ctx, req.RunID, req.NodeID)
		if err == nil {
			return n, nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	return nil, err
}

// consumeBounce closes one request, retrying briefly.
//
// "A request must not outlive the kill it asked for" is the invariant,
// and a single dropped write would break it: the row would stay
// pending, and the next dispatch of this node id would read it as a
// fresh instruction to kill an attempt nobody bounced. Retrying is
// what makes the sentence true rather than usual.
func (r *Runner) consumeBounce(ctx context.Context, req runner.Request, seq int64, outcome string) error {
	var err error
	for attempt := range bounceConsumeAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(bounceRetryDelay):
			}
		}
		if err = r.ctrl.ConsumeNodeBounce(ctx, req.RunID, req.NodeID, seq, outcome); err == nil {
			return nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
	}
	r.cfg.Logger.Warn("local runner: consume bounce request",
		"run_id", req.RunID, "node_id", req.NodeID, "seq", seq, "err", err)
	return err
}

// settleOpenBounces closes every request still open for this node once
// the runner is done with it.
//
// A pending request is an instruction, not a note. The runner that
// takes the node next -- an auto-retry is a fresh RunNode on the same
// node id -- polls the same rows and would read a request meant for an
// attempt that is already over as one meant for its own, killing a
// process nobody asked it to kill. So a request this runner did not
// honor is closed as missed: the node ended, and the kill was never
// delivered. One this runner did honor but failed to record is closed
// with the verdict it actually reached, which the ledger remembers.
//
// It runs after the poll has stopped, so nothing can open a request
// behind it, and it is bounded so a store that refuses to accept the
// close cannot spin here.
func (r *Runner) settleOpenBounces(ctx context.Context, req runner.Request, ledger bounceLedger) {
	for range bounceSweepLimit {
		b, err := r.ctrl.PendingNodeBounce(ctx, req.RunID, req.NodeID)
		if err != nil {
			r.cfg.Logger.Warn("local runner: sweep bounce requests",
				"run_id", req.RunID, "node_id", req.NodeID, "err", err)
			return
		}
		if b == nil {
			return
		}
		outcome, honored := ledger[b.Seq]
		if !honored {
			outcome = store.BounceMissed
			payload, _ := json.Marshal(map[string]any{
				"seq":                      b.Seq,
				"requested_by":             b.RequestedBy,
				"requested_at":             b.RequestedAt,
				"admission_lease_retained": true,
				"reason":                   "the node ended before the kill was delivered",
			})
			if evErr := r.ctrl.AppendEvent(ctx, req.RunID, req.NodeID,
				"node_bounce_missed", payload); evErr != nil {
				r.cfg.Logger.Warn("local runner: record bounce event",
					"run_id", req.RunID, "node_id", req.NodeID, "err", evErr)
			}
		}
		if err := r.consumeBounce(ctx, req, b.Seq, outcome); err != nil {
			return
		}
		ledger[b.Seq] = outcome
	}
}

// addUsage folds one attempt's accounting into the node's running
// total: CPU and occupancy add up because the machine paid for each
// attempt, while peak RSS takes the high-water because the attempts
// did not hold their peaks at the same time. It is the arithmetic the
// store applies across separate writes, done here because a bounced
// attempt's figures reach the store no other way -- the attempt writes
// no row of its own.
func addUsage(total, attempt *runner.ResourceUsage) *runner.ResourceUsage {
	if attempt == nil {
		return total
	}
	if total == nil {
		copied := *attempt
		return &copied
	}
	total.CPUTime += attempt.CPUTime
	total.Wall += attempt.Wall
	if attempt.MaxRSSBytes > total.MaxRSSBytes {
		total.MaxRSSBytes = attempt.MaxRSSBytes
	}
	return total
}

// resultFor decides the node's outcome.
//
// The row the node process wrote wins outright: the process that ran
// the work is the authority on how it went, and an exit status only
// says how the process ended. A synthesized outcome is what is left
// when the process died without writing one, and it is written back so
// the run does not carry a node stuck at "running".
func (r *Runner) resultFor(ctx context.Context, req runner.Request, cmd *exec.Cmd, waitErr error, cancelled bool, usage *runner.ResourceUsage) runner.Result {
	readCtx := context.WithoutCancel(ctx)

	n, err := r.ctrl.GetNode(readCtx, req.RunID, req.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		r.cfg.Logger.Warn("local runner: read node row",
			"run_id", req.RunID, "node_id", req.NodeID, "err", err)
	}
	if err == nil && runner.NodeTerminal(n) {
		res := runner.ResultFromNode(n)
		res.Usage = usage
		return res
	}

	verdict := classifyExit(req.NodeID, exitInfoFrom(cmd.ProcessState, waitErr), cancelled, usage)
	verdict.result.Usage = usage
	if verdict.result.Outcome == sparkwing.Cancelled {
		// safety: teardown owns the cancelled node's row; writing one here would
		// race the classifier that records whether the run was cancelled
		// or superseded.
		return verdict.result
	}
	if ferr := r.ctrl.FinishNodeWithReason(readCtx, req.RunID, req.NodeID,
		string(verdict.result.Outcome), verdict.message, nil, verdict.reason, verdict.exitCode); ferr != nil {
		r.cfg.Logger.Warn("local runner: finish node after process exit",
			"run_id", req.RunID, "node_id", req.NodeID, "err", ferr)
	}
	return verdict.result
}

// supervise watches a live node for as long as this runner owns it:
// it keeps the node's heartbeat fresh and asks the controller whether
// an operator has requested a bounce.
//
// It spans every attempt rather than one, because the node is what it
// watches and the node survives a bounce. A heartbeat that stopped
// while the process was being replaced would let the reaper conclude
// the node had been abandoned.
//
// A request is handed over once. The channel is the handover and seq
// is what keeps it to one: a request already passed to an attempt is
// skipped on later ticks, so polling cannot turn one intent into two
// kills. A send that would block is left for the next tick, which is
// the window where an attempt is between spawns.
func (r *Runner) supervise(ctx context.Context, req runner.Request, bounces chan<- *store.NodeBounce) {
	_ = r.ctrl.TouchNodeHeartbeat(ctx, req.RunID, req.NodeID)
	t := time.NewTicker(r.cfg.SuperviseInterval)
	defer t.Stop()
	var handed int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := r.ctrl.TouchNodeHeartbeat(ctx, req.RunID, req.NodeID); err != nil {
			r.cfg.Logger.Debug("local runner: heartbeat failed",
				"run_id", req.RunID, "node_id", req.NodeID, "err", err)
		}
		b, err := r.pollBounce(ctx, req)
		if err != nil {
			r.cfg.Logger.Debug("local runner: poll for bounce request",
				"run_id", req.RunID, "node_id", req.NodeID, "err", err)
			continue
		}
		if b == nil || b.Seq <= handed {
			continue
		}
		select {
		case bounces <- b:
			handed = b.Seq
		default:
		}
	}
}

// forwardRecords replays the child's NDJSON log stream into the run's
// live delegate, so a node in its own process reads on the terminal
// exactly as it did when it shared the dispatcher's.
//
// A line that does not decode is forwarded as a warn record rather
// than dropped: it is output the node produced, and something that
// writes to stdout beside the logger (a subprocess, a stray print) is
// the case where losing it hurts most.
func forwardRecords(out io.Reader, req runner.Request, logger *slog.Logger) {
	if req.Delegate == nil {
		_, _ = io.Copy(io.Discard, out)
		return
	}
	err := forwardLines(out, func(line []byte, truncated bool) {
		if len(bytes.TrimSpace(line)) == 0 {
			return
		}
		// safety: a truncated line is a fragment, so any JSON it began
		// is now unterminated. Parsing is not attempted -- a fragment
		// that happened to parse would report a record the node never
		// emitted.
		if !truncated {
			var rec sparkwing.LogRecord
			if json.Unmarshal(line, &rec) == nil {
				if rec.TS.IsZero() {
					rec.TS = time.Now()
				}
				if rec.JobID == "" {
					rec.JobID = req.NodeID
				}
				req.Delegate.Emit(rec)
				return
			}
		}
		req.Delegate.Emit(rawLineRecord(req.NodeID, string(line), truncated))
	})
	if err != nil && logger != nil {
		logger.Debug("local runner: stdout forward ended", "node_id", req.NodeID, "err", err)
	}
}

// forwardStderr mirrors the child's stderr line by line. Nothing on
// that stream is structured, so every line is a warning: a node
// process only writes there when something bypassed the logger.
func forwardStderr(errOut io.Reader, req runner.Request) {
	if req.Delegate == nil {
		_, _ = io.Copy(io.Discard, errOut)
		return
	}
	_ = forwardLines(errOut, func(line []byte, truncated bool) {
		text := strings.TrimRight(string(line), "\r")
		if strings.TrimSpace(text) == "" {
			return
		}
		req.Delegate.Emit(rawLineRecord(req.NodeID, text, truncated))
	})
}

// rawLineRecord wraps one unstructured output line as a warn record,
// flagging a line the forwarder had to cut short so a reader is not
// left to wonder why the text stops mid-token.
func rawLineRecord(nodeID, text string, truncated bool) sparkwing.LogRecord {
	rec := sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "warn",
		JobID: nodeID,
		Msg:   text,
	}
	if truncated {
		rec.Msg += truncationMarker
		rec.Attrs = map[string]any{"truncated": true, "limit_bytes": maxLogLineBytes}
	}
	return rec
}

// forwardLines splits out into lines and hands each to emit, reading
// until EOF no matter what any single line contains.
//
// It cannot use bufio.Scanner, and the reason is a deadlock rather
// than a style preference. Scanner.Scan reports false on a line past
// its buffer cap, which ends the caller's range loop -- so the
// forwarder stops draining a pipe the child is still writing to. The
// child then blocks forever in write(2), and the node's own timeout
// cannot fire because that timeout runs inside the blocked child. One
// pathological line would hang the run with no diagnosis available.
//
// So an oversized line is truncated, not fatal: the first
// maxLogLineBytes are emitted with truncated=true, the rest of the
// line is discarded up to the next newline, and reading continues.
// The returned error is the read error that ended the stream, or nil
// at EOF.
func forwardLines(out io.Reader, emit func(line []byte, truncated bool)) error {
	r := bufio.NewReaderSize(out, lineReadBufferBytes)
	var line []byte
	truncated := false

	flush := func() {
		emit(line, truncated)
		line = line[:0]
		truncated = false
	}

	for {
		chunk, err := r.ReadSlice('\n')
		complete := err == nil
		if complete {
			chunk = chunk[:len(chunk)-1]
		}
		if room := maxLogLineBytes - len(line); room > 0 {
			if len(chunk) > room {
				chunk, truncated = chunk[:room], true
			}
			line = append(line, chunk...)
		} else if len(chunk) > 0 {
			truncated = true
		}

		switch {
		case complete:
			flush()
		case errors.Is(err, bufio.ErrBufferFull):
			// safety: more of the same line follows; keep reading it.
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				flush()
			}
			return nil
		default:
			// safety: complete is err == nil, so every remaining case has a
			// real read error.
			if len(line) > 0 {
				flush()
			}
			return err
		}
	}
}

// maxLogLineBytes caps one forwarded line. A node that prints a
// megabyte on one line is pathological; carrying the excess would
// grow the dispatcher's heap to match whatever the node chose to
// print.
const maxLogLineBytes = 1 << 20

// lineReadBufferBytes is how much of a long line the reader holds at
// once. It bounds a single read, not a line: forwardLines reassembles
// across as many buffer-fulls as a line needs.
const lineReadBufferBytes = 64 * 1024

// truncationMarker is appended to a line the forwarder cut short.
const truncationMarker = " …[truncated: line exceeded the 1MiB forwarding limit]"

func failedResult(err error) runner.Result {
	return runner.Result{Outcome: sparkwing.Failed, Err: err}
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
