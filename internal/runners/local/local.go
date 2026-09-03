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

const ParentLivenessFD = 3

const ParentLivenessFDEnv = "SPARKWING_PARENT_LIVENESS_FD"

const superviseInterval = 5 * time.Second

const bouncePollTimeout = 2 * time.Second

const bounceRetryDelay = 200 * time.Millisecond

const (
	bounceReadAttempts = 3

	bounceConsumeAttempts = 3

	bounceSweepLimit = 8
)

type Config struct {
	Executable string

	ControllerURL string
	AgentToken    string

	// APISocket is the admission daemon's controller API socket when the
	// run's state lives behind it. A node process that gets one dials it
	// and sends no bearer token, because the daemon takes the connection's
	// peer uid as the principal.
	APISocket string

	WorkDir string

	Home string

	CacheURL string

	LeaseTokens func(context.Context) (lease, child string)

	DryRun  bool
	StartAt string
	StopAt  string

	TerminationGrace time.Duration

	SuperviseInterval time.Duration

	Labels []string

	Logger *slog.Logger
}

type Runner struct {
	cfg  Config
	ctrl *client.Client
}

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

func (r *Runner) SetLeaseTokenSource(fn func(context.Context) (string, string)) {
	r.cfg.LeaseTokens = fn
}

func (r *Runner) AdvertisedLabels() []string {
	out := make([]string, len(r.cfg.Labels))
	copy(out, r.cfg.Labels)
	return out
}

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

func (r *Runner) pollBounce(ctx context.Context, req runner.Request) (*store.NodeBounce, error) {
	pollCtx, cancel := context.WithTimeout(ctx, bouncePollTimeout)
	defer cancel()
	return r.ctrl.PendingNodeBounce(pollCtx, req.RunID, req.NodeID)
}

type bounceLedger map[int64]string

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

type attemptEnding int

const (
	endExited attemptEnding = iota

	endCancelled

	endBounced
)

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

type bounceVerdict int

const (
	bounceRespawn bounceVerdict = iota

	bounceMissed

	bounceTornDown
)

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

// safety: keep draining after an oversized line; Scanner would stop, block the
// child in write(2), and prevent the node timeout from firing.
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

const maxLogLineBytes = 1 << 20

const lineReadBufferBytes = 64 * 1024

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
