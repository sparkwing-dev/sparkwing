package nodehost

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// NodeLog is one node's log for one attempt. The orchestrator's node logs
// satisfy it.
type NodeLog interface {
	RecordWriter
	Close() error
}

// LossRecorder is a [NodeLog] that records lines the attempt's process wrote
// but the log never received, so it seals as incomplete rather than complete.
type LossRecorder interface {
	RecordLoss(lines int)
}

// Attempts is what the default executor needs to run each attempt in its own
// SDK process: the node routes the process reaches, where the node's secrets
// resolve from, the run's masker, and where its log goes.
type Attempts struct {
	Routes   *Server
	Resolver secretsource.Resolver
	Masker   *secrets.Masker
	Local    LocalOutputs
	// Children starts the child runs of a run no controller holds; nil
	// answers the child routes 502 there.
	Children LocalChildren
	// OpenLog opens the node's log; the executor closes it once the attempt's
	// last record, cleanup output included, is written.
	OpenLog func(ctx context.Context, runID, nodeID string) (NodeLog, error)
	// Usage receives the measured session usage of each process bound to a
	// node, its eval processes included; nil drops it. [RecordUsage] builds
	// the one that records it.
	Usage func(ctx context.Context, n *NodeRun, u Usage)
}

func (a *Attempts) executor(cfg *Config) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		return a.process(ctx, cfg.Session, n, func(ctx context.Context, sess *ProtocolSession, sink *LogSink) Result {
			return drive(ctx, sess, sink, n)
		})
	}
}

type nodeProcessBody func(ctx context.Context, sess *ProtocolSession, sink *LogSink) Result

// safety: every message the process returns is masked, since the scheduler
// stores it in the node row, past the log sink's masking.
func (a *Attempts) process(ctx context.Context, base SessionConfig, n *NodeRun, body nodeProcessBody) Result {
	res := a.attempt(ctx, base, n, body)
	if res.Err != nil {
		res.Err = errors.New(a.Masker.Mask(res.Err.Error()))
	}
	return res
}

// safety: the bearer is revoked before the cleanups run, so the process
// cannot register a cleanup that never runs.
func (a *Attempts) attempt(ctx context.Context, base SessionConfig, n *NodeRun, body nodeProcessBody) Result {
	run, id := n.Run, n.Plan.ID
	base.Masker = a.Masker
	env, cleanupEnv, err := attemptEnvironments(base, n)
	if err != nil {
		return failed(err)
	}
	// safety: a node's eval processes and its attempts each open its log, so OpenLog must append.
	nlog, err := a.OpenLog(ctx, run.ID, id)
	if err != nil {
		return failed(fmt.Errorf("open node log: %w", err))
	}
	defer func() {
		if err := nlog.Close(); err != nil {
			run.Logger.Warn("nodehost: close node log", "run", run.ID, "node", id, "err", err)
		}
	}()
	sink, err := NewLogSink(ctx, LogSinkConfig{RunID: run.ID, NodeID: id, Log: nlog, State: run.State, Masker: a.Masker, Progress: n.Progress})
	if err != nil {
		return failed(err)
	}
	names := run.Secrets
	bearer, revoke, err := a.Routes.Admit(Node{
		RunID: run.ID, NodeID: id, Secrets: names, Resolver: a.Resolver, Masker: a.Masker, Local: a.Local, Refs: n.Plan.References,
		Children: a.Children, Pause: n.Pause, Hold: n.Hold, Output: n.memoOutput,
		Job: n.Plan, Sink: sink,
	})
	if err != nil {
		return failed(fmt.Errorf("admit node %s: %w", id, err))
	}
	// safety: registered before the process starts, so an environment dump never logs a live bearer.
	a.Masker.Register(bearer)
	cfg := base
	cfg.Sink, cfg.Masker = sink, a.Masker
	cfg.Env = env
	cfg.Binding = &nodeproto.ProcessBinding{RunID: run.ID, JobID: id, Routes: nodeproto.ProcessRoutes{URL: a.Routes.URL(), Bearer: bearer}}
	res := Result{}
	transport, output, err := cfg.protocol(func() error { revoke(); return nil })
	if err != nil {
		revoke()
		return failed(err)
	}
	sess, err := StartProtocolSession(ctx, transport)
	var sampler *UsageSampler
	if sess != nil {
		sampler = StartUsage(sess.cmd.Process.Pid)
		if err == nil {
			err = cfg.hello(ctx, sess)
		}
	}
	var usage Usage
	if err != nil {
		if sess != nil {
			err = errors.Join(err, sess.Close())
			usage = sampler.Stop(sess.cmd.ProcessState)
		} else {
			output.close()
		}
	}
	if err == nil {
		stop := context.AfterFunc(ctx, func() { sess.failProtocol(context.Cause(ctx)) })
		res = body(ctx, sess, sink)
		cerr := sess.Close()
		stop()
		usage = sampler.Stop(sess.cmd.ProcessState)
		if lost, ok := nlog.(LossRecorder); ok && output.lost > 0 {
			lost.RecordLoss(output.lost)
		}
		var perr *apierr.Error
		switch {
		case cerr == nil || res.Outcome != sparkwing.Success:
		case errors.As(cerr, &perr):
			// safety: a session that broke the protocol after its reply cannot vouch for that reply.
			res = failed(cerr)
		default:
			run.Logger.Warn("nodehost: sdk session", "run", run.ID, "node", id, "err", cerr)
		}
		if a.Usage != nil {
			a.Usage(ctx, n, usage)
		}
	} else {
		res = failed(err)
	}
	revoke()
	out := NewLineWriter(sink, LevelInfo, a.Masker)
	// safety: a cancelled node's cleanups still run, each bounded by CleanupTimeout.
	if err := a.Routes.RunCleanups(context.WithoutCancel(ctx), bearer, CleanupScope{Dir: cfg.Dir, Env: cleanupEnv, Log: out}); err != nil {
		run.Logger.Warn("nodehost: node cleanups", "run", run.ID, "node", id, "err", err)
	}
	out.Flush()
	return res
}

func drive(ctx context.Context, sess *ProtocolSession, sink *LogSink, n *NodeRun) Result {
	if err := nodeReplan(ctx, sess, n); err != nil {
		return failed(err)
	}
	return withClosures(ctx, sess, sink, n, func() Result { return attemptBody(ctx, sess, sink, n) })
}

func nodeReplan(ctx context.Context, sess *ProtocolSession, n *NodeRun) error {
	run := n.Run
	args := run.Args
	if args == nil {
		args = map[string]string{}
	}
	rc := run.Context
	var raw json.RawMessage
	err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: cmp.Or(run.Entrypoint, run.Pipeline), Args: args, Run: &rc}, &raw)
	switch {
	case err != nil:
		return err
	case len(n.Generators) > 0:
		return regenerate(ctx, sess, n)
	}
	return checkDrift(raw, n.Plan, n.Run.validation)
}

func attemptBody(ctx context.Context, sess *ProtocolSession, sink *LogSink, n *NodeRun) Result {
	return driveSteps(ctx, sess, sink, n, max(n.Attempt, 1))
}

// safety: the node runs only as the run accepted it, so a process that plans
// it differently, or not at all, fails the attempt before its body starts.
func checkDrift(raw json.RawMessage, accepted nodeproto.JobSpec, validation nodeproto.ValidationContext) error {
	doc, err := nodeproto.ValidatePlan(raw, validation)
	if err != nil {
		return err
	}
	for _, got := range doc.Jobs {
		if got.ID == accepted.ID && jobHash(got) == jobHash(accepted) {
			return nil
		}
	}
	return &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf("job %s differs from its accepted specification", accepted.ID)}
}

// safety: an SDK refusal with no code is recorded as sdk_error, and a body's own error has no reason.
func failed(err error) Result {
	reason := ""
	var perr *apierr.Error
	if errors.As(err, &perr) {
		reason = string(cmp.Or(perr.Code, apierr.SDKError))
	}
	return Result{Outcome: sparkwing.Failed, Err: err, Reason: reason}
}

func buildSessionEnvironment(cfg SessionConfig, kind procenv.Kind, runtime procenv.Runtime) ([]string, error) {
	base := cfg.Env
	if base == nil {
		base = engineenv.Minimal()
	}
	return procenv.Build(kind, procenv.Base{Local: true, Captured: base, Masker: cfg.Masker}, runtime)
}

func attemptEnvironments(cfg SessionConfig, n *NodeRun) ([]string, []string, error) {
	runtime := sdkRuntime(n.Run.Context, cfg.Runner)
	runtime.RunID, runtime.JobID = n.Run.ID, n.Plan.ID
	return projectJobEnvironment(procenv.Attempt, n.Plan.Env, runtime, true, nil, func(kind procenv.Kind, runtime procenv.Runtime) ([]string, error) {
		return buildSessionEnvironment(cfg, kind, runtime)
	})
}

// LineWriter masks what is written to it as one text stream and hands each
// line to a [LogSink] at one level; a line that grows past the session's line
// limit goes in pieces, masked whole across them. Call Flush once writing
// ends. It is unlocked, so one goroutine writes at a time.
type LineWriter struct {
	sink   interface{ Text(string, string) }
	level  string
	stream *secrets.Stream
	buf    []byte
}

// NewLineWriter returns a writer whose lines reach sink at level, masked with masker.
func NewLineWriter(sink *LogSink, level string, masker *secrets.Masker) *LineWriter {
	return &LineWriter{sink: sink, level: level, stream: masker.Stream()}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.sink.Text(w.level, w.stream.Mask(strings.TrimSuffix(string(w.buf[:i]), "\r"), true))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) >= maxTextLine {
		w.sink.Text(w.level, w.stream.Mask(string(w.buf), false))
		w.buf = nil
	}
	return len(p), nil
}

// Flush hands the unterminated rest, and any tail held for it, to the sink.
func (w *LineWriter) Flush() {
	w.sink.Text(w.level, w.stream.Mask(string(w.buf), true))
	w.buf = nil
}
