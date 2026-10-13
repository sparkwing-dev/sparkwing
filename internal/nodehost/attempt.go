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

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
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

// Attempts is what the default executor needs to run each attempt in its own
// SDK process: the node routes the process reaches, where the node's secrets
// resolve from, the run's masker, and where its log goes.
type Attempts struct {
	Routes   *Server
	Resolver secretsource.Resolver
	Masker   *secrets.Masker
	// Claim forwards the controller-backed routes; nil answers them 502.
	Claim *Claim
	// OpenLog opens the node's log; the executor closes it once the attempt's
	// last record, cleanup output included, is written.
	OpenLog func(ctx context.Context, runID, nodeID string) (NodeLog, error)
	// Usage receives each attempt's measured session usage; nil drops it.
	Usage func(n *NodeRun, u Usage)
}

// safety: every message the attempt returns is masked, since the scheduler
// stores it in the node row, past the log sink's masking.
func (a *Attempts) executor(cfg *Config) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		res := a.attempt(ctx, cfg.Session, n)
		if res.Err != nil {
			res.Err = errors.New(a.Masker.Mask(res.Err.Error()))
		}
		return res
	}
}

// safety: the bearer is revoked before the cleanups run, so the process
// cannot register a cleanup that never runs.
func (a *Attempts) attempt(ctx context.Context, base SessionConfig, n *NodeRun) Result {
	run, id := n.Run, n.Plan.ID
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
		RunID: run.ID, NodeID: id, Secrets: names, Resolver: a.Resolver, Masker: a.Masker, Claim: a.Claim,
		Pause: n.Pause,
	})
	if err != nil {
		return failed(fmt.Errorf("admit node %s: %w", id, err))
	}
	// safety: registered before the process starts, so an environment dump never logs a live bearer.
	a.Masker.Register(bearer)
	cfg := base
	cfg.Sink = sink
	cfg.Env = attemptEnv(base.Env, n.Plan.Env, run.ID, id, a.Routes.URL(), bearer)
	res := Result{}
	sess, err := StartSession(ctx, cfg)
	if err == nil {
		res = drive(ctx, sess, n)
		usage, cerr := sess.Close()
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
			a.Usage(n, usage)
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

func drive(ctx context.Context, sess *Session, n *NodeRun) Result {
	run := n.Run
	args := run.Args
	if args == nil {
		args = map[string]string{}
	}
	rc := run.Context
	var raw json.RawMessage
	err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: run.Pipeline, Args: args, Run: &rc}, &raw)
	if err == nil {
		err = checkDrift(raw, n.Plan)
	}
	if err != nil {
		return failed(err)
	}
	attempt := max(n.Attempt, 1)
	var rr nodeproto.RunResult
	err = ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpRunNode, Node: n.Plan.ID, Attempt: attempt, Failure: n.Failure}, &rr)
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

// safety: the one list of engine variables a process inherits when its caller passes no environment.
var minimalEnv = []string{
	"PATH", "HOME", "TMPDIR", "LANG", "LC_*", "TZ", "USER",
	"SystemRoot", "ComSpec", "PATHEXT", "TEMP", "TMP",
}

// MinimalEnv is the engine's environment cut to the variables a process
// needs to run at all: PATH, HOME, TMPDIR, LANG, LC_*, TZ and USER, and on
// Windows SystemRoot, ComSpec, PATHEXT, TEMP and TMP.
func MinimalEnv() []string {
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if slices.ContainsFunc(minimalEnv, func(name string) bool {
			prefix, wild := strings.CutSuffix(name, "*")
			if runtime.GOOS == "windows" {
				k, name, prefix = strings.ToUpper(k), strings.ToUpper(name), strings.ToUpper(prefix)
			}
			return k == name || (wild && strings.HasPrefix(k, prefix))
		}) {
			env = append(env, kv)
		}
	}
	return env
}

// safety: the engine's own SPARKWING_* variables never reach the node; its
// contract variables are set last so neither the engine nor the node's env
// overrides them.
func attemptEnv(base []string, nodeEnv map[string]string, runID, nodeID, url, bearer string) []string {
	if base == nil {
		base = MinimalEnv()
	}
	env := make([]string, 0, len(base)+len(nodeEnv)+4)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "SPARKWING_") {
			env = append(env, kv)
		}
	}
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
