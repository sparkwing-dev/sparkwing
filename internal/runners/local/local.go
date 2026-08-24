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

// heartbeatInterval matches the cluster runner's: a missed heartbeat
// is a dashboard annoyance, not a correctness problem.
const heartbeatInterval = 5 * time.Second

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
// outcome.
func (r *Runner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	cmd := exec.Command(r.cfg.Executable, nodeArgv(r.cfg.ControllerURL, req.RunID, req.NodeID)...)
	cmd.Dir = r.cfg.WorkDir
	cmd.Env = childEnv(ctx, os.Environ(), r.cfg, req)

	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		return failedResult(fmt.Errorf("local runner: stdout pipe for %s: %w", req.NodeID, err))
	}
	stderr, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdout, stdoutW)
		return failedResult(fmt.Errorf("local runner: stderr pipe for %s: %w", req.NodeID, err))
	}
	livenessR, livenessW, err := os.Pipe()
	if err != nil {
		closeAll(stdout, stdoutW, stderr, stderrW)
		return failedResult(fmt.Errorf("local runner: liveness pipe for %s: %w", req.NodeID, err))
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
		return failedResult(fmt.Errorf("local runner: spawn %s: %w", req.NodeID, err))
	}

	var forwarders sync.WaitGroup
	forwarders.Add(2)
	go func() { defer forwarders.Done(); forwardRecords(stdout, req, r.cfg.Logger) }()
	go func() { defer forwarders.Done(); forwardStderr(stderr, req) }()

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go r.heartbeat(hbCtx, req)
	_ = r.ctrl.UpdateNodeActivity(ctx, req.RunID, req.NodeID,
		fmt.Sprintf("running, pid %d", group.ID()))

	waitErr, cancelled := r.await(ctx, group)
	wall := time.Since(spawnedAt)
	forwarders.Wait()
	closeAll(stdout, stderr)
	stopHB()

	return r.resultFor(ctx, req, cmd, waitErr, cancelled, wall)
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

// await waits for the process tree, terminating it when the run is
// cancelled. It reports the wait error and whether cancellation, not
// the node itself, ended the process.
func (r *Runner) await(ctx context.Context, group *procgroup.Group) (error, bool) {
	done := make(chan error, 1)
	go func() { done <- group.Finish(context.WithoutCancel(ctx), r.cfg.TerminationGrace) }()

	select {
	case err := <-done:
		return err, false
	case <-ctx.Done():
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*r.cfg.TerminationGrace+30*time.Second)
	defer cancel()
	if err := group.Terminate(termCtx, r.cfg.TerminationGrace); err != nil && !errors.Is(err, procgroup.ErrCleanup) {
		r.cfg.Logger.Debug("local runner: terminate", "err", err)
	}
	return <-done, true
}

// resultFor decides the node's outcome.
//
// The row the node process wrote wins outright: the process that ran
// the work is the authority on how it went, and an exit status only
// says how the process ended. A synthesized outcome is what is left
// when the process died without writing one, and it is written back so
// the run does not carry a node stuck at "running".
func (r *Runner) resultFor(ctx context.Context, req runner.Request, cmd *exec.Cmd, waitErr error, cancelled bool, wall time.Duration) runner.Result {
	readCtx := context.WithoutCancel(ctx)
	usage := usageFrom(cmd.ProcessState)
	if usage != nil {
		usage.Wall = wall
	}

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

func (r *Runner) heartbeat(ctx context.Context, req runner.Request) {
	_ = r.ctrl.TouchNodeHeartbeat(ctx, req.RunID, req.NodeID)
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.ctrl.TouchNodeHeartbeat(ctx, req.RunID, req.NodeID); err != nil {
				r.cfg.Logger.Debug("local runner: heartbeat failed",
					"run_id", req.RunID, "node_id", req.NodeID, "err", err)
			}
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
