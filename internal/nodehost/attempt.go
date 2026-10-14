package nodehost

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/secretsource"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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
	// Claim forwards the controller-backed routes; nil answers them 502,
	// except the output routes Local answers.
	Claim *Claim
	Local LocalOutputs
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
		return a.process(ctx, cfg.Session, n, func(ctx context.Context, sess *Session, sink *LogSink) Result {
			return drive(ctx, sess, sink, n)
		})
	}
}

type nodeProcessBody func(ctx context.Context, sess *Session, sink *LogSink) Result

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
	names := make([]string, 0, len(run.Doc.Secrets))
	for _, s := range run.Doc.Secrets {
		names = append(names, s.Name)
	}
	bearer, revoke, err := a.Routes.Admit(Node{
		RunID: run.ID, NodeID: id, Secrets: names, Resolver: a.Resolver, Masker: a.Masker, Claim: a.Claim, Local: a.Local,
		Children: a.Children, LeaseToken: childLease(n), Pause: n.Pause, Hold: n.Hold, Output: n.memoOutput,
	})
	if err != nil {
		return failed(fmt.Errorf("admit node %s: %w", id, err))
	}
	// safety: registered before the process starts, so an environment dump never logs a live bearer.
	a.Masker.Register(bearer)
	cfg := base
	cfg.Sink = sink
	cfg.Env = attemptEnv(base.Env, n.Plan.Env, run.ID, id, a.Routes.URL(), bearer)
	cfg.Env = append(append(withoutRunnerEnv(cfg.Env), hostEnv(n)...), runnerEnv()...)
	res := Result{}
	sess, err := StartSession(ctx, cfg)
	if err == nil {
		res = body(ctx, sess, sink)
		usage, cerr := sess.Close()
		if lost, ok := nlog.(LossRecorder); ok && sess.Lost() > 0 {
			lost.RecordLoss(sess.Lost())
		}
		var perr *nodeproto.Error
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
	sink.Flush()
	revoke()
	out := &lineWriter{sink: sink}
	// safety: a cancelled node's cleanups still run, each bounded by CleanupTimeout.
	if err := a.Routes.RunCleanups(context.WithoutCancel(ctx), bearer, CleanupScope{Dir: cfg.Dir, Env: cfg.Env, Log: out}); err != nil {
		run.Logger.Warn("nodehost: node cleanups", "run", run.ID, "node", id, "err", err)
	}
	out.flush()
	sink.Flush()
	return res
}

func drive(ctx context.Context, sess *Session, sink *LogSink, n *NodeRun) Result {
	if err := nodeReplan(ctx, sess, n); err != nil {
		return failed(err)
	}
	return withClosures(ctx, sess, sink, n, func() Result { return attemptBody(ctx, sess, sink, n) })
}

func nodeReplan(ctx context.Context, sess *Session, n *NodeRun) error {
	run := n.Run
	args := run.Args
	if args == nil {
		args = map[string]string{}
	}
	rc := run.Context
	var raw json.RawMessage
	err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: run.Pipeline, Args: args, Run: &rc}, &raw)
	switch {
	case err != nil:
		return err
	case n.Generator != "":
		return regenerate(ctx, sess, n)
	}
	return checkDrift(raw, n.Plan)
}

func attemptBody(ctx context.Context, sess *Session, sink *LogSink, n *NodeRun) Result {
	attempt := max(n.Attempt, 1)
	if w := n.Plan.Work; w != nil && len(w.Steps) > 0 {
		return driveSteps(ctx, sess, sink, n, attempt)
	}
	var rr nodeproto.RunResult
	err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpRunNode, Node: n.Plan.ID, Attempt: attempt, Failure: n.Failure}, &rr)
	if err == nil {
		err = rr.Validate()
	}
	switch {
	case err != nil:
		return failed(err)
	case rr.Outcome == nodeproto.OutcomeFailed:
		msg := rr.Error
		if msg == "" {
			msg = "node body failed"
		}
		return failed(errors.New(msg))
	}
	return Result{Outcome: sparkwing.Success, Output: rr.Output}
}

// safety: the node runs only as the run accepted it, so a process that plans
// it differently, or not at all, fails the attempt before its body starts.
func checkDrift(raw json.RawMessage, accepted nodeproto.PlanNode) error {
	stamped, err := nodeproto.StampSpecHashes(raw)
	if err != nil {
		return err
	}
	var doc struct {
		Protocol string `json:"protocol"`
		Nodes    []struct {
			ID       string `json:"id"`
			SpecHash string `json:"spec_hash"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(stamped, &doc); err != nil {
		return fmt.Errorf("%w: plan document: %w", nodeproto.ErrMalformed, err)
	}
	if doc.Protocol != nodeproto.Version {
		return mismatch("plan", doc.Protocol)
	}
	drift := func(format string, args ...any) error {
		return &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(format, args...)}
	}
	if accepted.SpecHash == "" {
		return drift("the accepted plan holds no spec_hash for node %s", accepted.ID)
	}
	for _, got := range doc.Nodes {
		if got.ID != accepted.ID {
			continue
		}
		if got.SpecHash != accepted.SpecHash {
			return drift("node %s plans differently than when the run was accepted; plan code must be deterministic for a run", accepted.ID)
		}
		return nil
	}
	return drift("node %s is not in this process's plan; plan code must be deterministic for a run", accepted.ID)
}

// safety: an SDK refusal with no code is recorded as sdk_error, and a body's own error has no reason.
func failed(err error) Result {
	reason := ""
	var perr *nodeproto.Error
	if errors.As(err, &perr) {
		reason = string(cmp.Or(perr.Code, nodeproto.CodeSDKError))
	}
	return Result{Outcome: sparkwing.Failed, Err: err, Reason: reason}
}

// safety: the dispatcher's rule for what a child attaches to: the run's lease when it holds the host or the
// plan's groups, which a node lease lacks, else the node's own lease, which holds the cores the node runs on.
func childLease(n *NodeRun) string {
	r := n.Run
	if hostAdmitted(r.Doc) || r.leaseHoldsGroups || r.leaseHoldsHost {
		return r.leaseToken
	}
	return cmp.Or(n.nodeLease, r.leaseToken)
}

// safety: a sparkwing run the node starts attaches to the lease the node holds instead of queueing behind it,
// and sparks-core echoes mutating commands instead of running them only when it sees SPARKWING_DRY_RUN.
func hostEnv(n *NodeRun) []string {
	var env []string
	if lease := childLease(n); lease != "" {
		env = append(env, wingwire.LeaseTokenEnv+"="+lease)
	}
	if n.Run.Context.DryRun {
		env = append(env, "SPARKWING_DRY_RUN=1")
	}
	return env
}

// safety: a pod host's Job sets the runner variables on the host itself; a host
// without them runs nodes on this machine, the local runner. A node process
// reads them for sparkwing.Runner and its tools for the runner's identity.
func runnerEnv() []string {
	name, typ := os.Getenv("SPARKWING_RUNNER_NAME"), os.Getenv("SPARKWING_RUNNER_TYPE")
	labels := os.Getenv("SPARKWING_RUNNER_LABELS")
	if name == "" && typ == "" && labels == "" {
		name, typ, labels = "local", "local", "local,os="+runtime.GOOS+",arch="+runtime.GOARCH
	}
	var env []string
	for _, kv := range [][2]string{{"SPARKWING_RUNNER_NAME", name}, {"SPARKWING_RUNNER_TYPE", typ}, {"SPARKWING_RUNNER_LABELS", labels}} {
		if kv[1] != "" {
			env = append(env, kv[0]+"="+kv[1])
		}
	}
	return env
}

// safety: a runner variable the host leaves unset must not survive from the caller's or the plan's env, or a
// node in a launcher Job could report the labels a pipeline wrote, such as local.
func withoutRunnerEnv(env []string) []string {
	return slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(strings.ToUpper(kv), "SPARKWING_RUNNER_")
	})
}

// safety: the plan process sees the dry-run switch its node processes see, so plan code that reads it plans
// the run each later process plans again, and no attempt fails with plan_drift.
func planSession(cfg Config) SessionConfig { return dryRunSession(cfg.Session, cfg.Run.DryRun) }

func dryRunSession(s SessionConfig, dryRun bool) SessionConfig {
	if dryRun {
		base := s.Env
		if base == nil {
			base = engineenv.Minimal()
		}
		s.Env = append(slices.Clone(base), "SPARKWING_DRY_RUN=1")
	}
	return s
}

// safety: the node's contract variables are set last so neither the engine nor the node's env overrides them.
func attemptEnv(base []string, nodeEnv map[string]string, runID, nodeID, url, bearer string) []string {
	if base == nil {
		base = engineenv.Minimal()
	}
	env := make([]string, 0, len(base)+len(nodeEnv)+4)
	env = append(env, engineenv.Strip(base)...)
	keys := make([]string, 0, len(nodeEnv))
	for k := range nodeEnv {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		env = append(env, k+"="+nodeEnv[k])
	}
	return append(env,
		"SPARKWING_RUN_ID="+runID,
		"SPARKWING_NODE_ID="+nodeID,
		protofd.NodeURLEnv+"="+url,
		protofd.NodeTokenEnv+"="+bearer,
	)
}

// safety: unlocked, because RunCleanups writes from one goroutine at a time.
type lineWriter struct {
	sink *LogSink
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.sink.Text(LevelInfo, strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) >= maxTextLine {
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.sink.Text(LevelInfo, string(w.buf))
		w.buf = nil
	}
}
