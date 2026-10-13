package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Config says how to host one run.
type Config struct {
	// Session starts the short-lived plan process.
	Session  SessionConfig
	Pipeline string
	// Args are the invocation's flag values; describe's defaults fill in
	// the flags it leaves out.
	Args map[string]string
	// Run is the run context plan code reads. RunID is required; a zero
	// StartedAt means now and an empty trigger source means manual.
	Run   nodeproto.RunContext
	State storage.StateStore
	// Artifacts holds the files nodes publish with outputs and stage with
	// consumes; nil turns both off.
	Artifacts storage.ArtifactStore
	// Execute runs one attempt of a node. Nil runs it with Attempts, and
	// fails every node it reaches when Attempts is nil too.
	Execute  Executor
	Attempts *Attempts
	Logger   *slog.Logger
	// Heartbeat is the run heartbeat interval; zero means 30 seconds.
	Heartbeat time.Duration
	// Admission admits each node before it runs; nil admits every node at once.
	Admission *Admission
}

// Result is how one node ended.
type Result struct {
	Outcome sparkwing.Outcome
	Err     error
	// Reason is the store's failure reason, such as [store.FailureVerify].
	Reason string
	Output json.RawMessage
}

// RunResult is how a hosted run ended. Status is success, failed or
// cancelled, and Err says why it is not success.
type RunResult struct {
	RunID  string
	Status string
	Err    error
	// Order lists the nodes in the order their executors started.
	Order []string
	Nodes map[string]Result
}

// Executor runs one node to a result. It owns no node row writes: the
// scheduler marks the node started just before the innermost executor runs
// and records the result it returns.
type Executor func(ctx context.Context, n *NodeRun) Result

// NodeRun is one node's turn through the executor chain.
type NodeRun struct {
	Run  *Run
	Plan nodeproto.PlanNode
	// Failure says how the parent failed; it is set only for an on_failure node.
	Failure *nodeproto.Failure
	// Attempt is the 1-based attempt number; zero means 1.
	Attempt int
}

// Run is the hosted run an executor works within.
type Run struct {
	ID string
	// Doc is the accepted plan. Nodes added with [Run.Add] are not in it.
	Doc nodeproto.PlanDoc
	// Pipeline, Args and Context are the accepted plan's request; every
	// later process of the run plans with the same.
	Pipeline string
	Args     map[string]string
	Context  nodeproto.RunContext
	State    storage.StateStore
	Logger   *slog.Logger
	adds     chan addRequest
}

type addRequest struct {
	ctx   context.Context
	nodes []nodeproto.PlanNode
	reply chan error
}

// Add validates nodes against the run's nodes, records their rows and
// schedules them. Only an executor of a running node of this run may call it.
func (r *Run) Add(ctx context.Context, nodes ...nodeproto.PlanNode) error {
	// safety: the reply is buffered so the scheduler never waits on a caller that gave up.
	req := addRequest{ctx: ctx, nodes: nodes, reply: make(chan error, 1)}
	select {
	case r.adds <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A Layer wraps the executor with one part of a node's envelope, such as
// caches or retries, or returns next unchanged when cfg does not enable that
// part. Each part adds its Layer to the scheduler's list in one line.
type Layer func(cfg *Config, next Executor) Executor

// safety: the order is the envelope's nesting, outermost first: approvals and recovery, debug pauses,
// admission, skip_if, memo, artifacts, caches, retries and timeouts, attempt closures, steps, expansion.
var layers = []Layer{
	admissionLayer,
	artifactsLayer,
	cachesLayer,
}

// Execute plans the run in a short-lived SDK process, records the run and
// its nodes, and runs the nodes in dependency order. An error means no run
// row was written; once one exists, every failure is in the result.
func Execute(ctx context.Context, cfg Config) (res RunResult, err error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Session.Sink == nil {
		cfg.Session.Sink = discardSink{}
	}
	if cfg.Run.RunID == "" {
		return RunResult{}, errors.New("nodehost: the run has no id")
	}
	if cfg.Run.StartedAt.IsZero() {
		cfg.Run.StartedAt = time.Now().UTC()
	}
	if cfg.Run.Trigger == nil {
		cfg.Run.Trigger = &nodeproto.Trigger{Source: "manual"}
	}
	sess, err := StartSession(ctx, cfg.Session)
	if err != nil {
		return RunResult{}, err
	}
	closeSession := func() {
		if _, err := sess.Close(); err != nil {
			cfg.Logger.Warn("nodehost: plan process", "err", err)
		}
	}
	args, err := describeArgs(ctx, sess, cfg.Pipeline, cfg.Args)
	if err == nil {
		err = cfg.State.CreateRun(ctx, runRow(cfg, args))
	}
	if err != nil {
		closeSession()
		return RunResult{}, err
	}

	res = RunResult{RunID: cfg.Run.RunID, Nodes: map[string]Result{}}
	finishCtx := context.WithoutCancel(ctx)
	// safety: registered before the heartbeat's defer so the heartbeat has
	// stopped before the run row goes terminal.
	defer func() {
		if ferr := cfg.State.FinishRun(finishCtx, res.RunID, res.Status, errText(res.Err)); ferr != nil {
			res.Status, res.Err = "failed", errors.Join(res.Err, fmt.Errorf("persist run state: %w", ferr))
		}
	}()
	stopHeartbeat := heartbeat(ctx, cfg, res.RunID)
	defer stopHeartbeat()

	doc, raw, err := planRun(ctx, sess, cfg.Pipeline, args, cfg.Run)
	closeSession()
	if err == nil {
		err = cfg.State.UpdatePlanSnapshot(ctx, res.RunID, raw)
	}
	if err != nil {
		res.Err = fmt.Errorf("plan: %w", err)
		if cause := context.Cause(ctx); cause != nil {
			res.Err = cause
		}
		res.Status = runStatus(res.Err)
		return res, nil
	}
	run := &Run{
		ID: res.RunID, Doc: doc, Pipeline: cfg.Pipeline, Args: args, Context: cfg.Run,
		State: cfg.State, Logger: cfg.Logger, adds: make(chan addRequest),
	}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(&cfg)}
	if err := s.add(ctx, ctx, doc.Nodes); err != nil {
		res.Status, res.Err = "failed", err
		return res, nil
	}
	s.loop(ctx)
	res.Order = s.order
	res.Status, res.Err = s.verdict(ctx)
	return res, nil
}

func runRow(cfg Config, args map[string]string) store.Run {
	rc := cfg.Run
	row := store.Run{
		ID: rc.RunID, Pipeline: cfg.Pipeline, Status: "running", Args: args,
		TriggerSource: rc.Trigger.Source, StartedAt: rc.StartedAt,
	}
	if g := rc.Git; g != nil {
		row.GitBranch, row.GitSHA, row.DeclaredRepo, row.RepoURL = g.Branch, g.SHA, g.Repo, g.RepoURL
		row.GithubOwner, row.GithubRepo = sparkwing.GithubOwnerRepo(g.Repo)
	}
	return row
}

func heartbeat(ctx context.Context, cfg Config, runID string) (stop func()) {
	interval := cfg.Heartbeat
	if interval <= 0 {
		interval = 30 * time.Second
	}
	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if err := cfg.State.TouchRunHeartbeat(hbCtx, runID); err != nil && hbCtx.Err() == nil {
				cfg.Logger.Warn("nodehost: run heartbeat", "run", runID, "err", err)
			}
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func chain(cfg *Config) Executor {
	exec := cfg.Execute
	if exec == nil && cfg.Attempts != nil {
		exec = cfg.Attempts.executor(cfg)
	}
	if exec == nil {
		exec = func(context.Context, *NodeRun) Result {
			return Result{Outcome: sparkwing.Failed, Err: errors.New("nodehost: no attempt executor")}
		}
	}
	inner := exec
	exec = func(ctx context.Context, n *NodeRun) Result {
		if err := n.Run.State.StartNode(ctx, n.Run.ID, n.Plan.ID); err != nil {
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("start node %s: %w", n.Plan.ID, err)}
		}
		return inner(ctx, n)
	}
	for i := len(layers) - 1; i >= 0; i-- {
		exec = layers[i](cfg, exec)
	}
	return exec
}

// safety: one goroutine owns the maps; executors report back over a channel.
type scheduler struct {
	run     *Run
	exec    Executor
	nodes   []nodeproto.PlanNode
	results map[string]Result
	started map[string]bool
	order   []string
}

type finished struct {
	id  string
	res Result
}

func (s *scheduler) loop(ctx context.Context) {
	done := make(chan finished)
	running := 0
	for {
		if s.startReady(ctx, done, &running) {
			continue
		}
		if running == 0 {
			// safety: validation refuses cycles, so a node still unstarted here
			// means the scheduler is wrong; it fails rather than reading as a success.
			for _, n := range s.nodes {
				if !s.started[n.ID] {
					s.terminal(ctx, n.ID, sparkwing.Failed, "nodehost: node never became ready")
				}
			}
			return
		}
		select {
		case f := <-done:
			running--
			s.finish(ctx, f.id, f.res)
		case req := <-s.run.adds:
			req.reply <- s.add(ctx, req.ctx, req.nodes)
		}
	}
}

func (s *scheduler) startReady(ctx context.Context, done chan<- finished, running *int) (progressed bool) {
	byID := make(map[string]nodeproto.PlanNode, len(s.nodes))
	for _, n := range s.nodes {
		byID[n.ID] = n
	}
	for _, n := range s.nodes {
		if s.started[n.ID] {
			continue
		}
		if ctx.Err() != nil {
			s.terminal(ctx, n.ID, sparkwing.Cancelled, "ctx-cancelled")
			progressed = true
			continue
		}
		waits := waitsOn(n, byID)
		if slices.ContainsFunc(waits, func(id string) bool { _, ok := s.results[id]; return !ok }) {
			continue
		}
		progressed = true
		node := &NodeRun{Run: s.run, Plan: n}
		if parent := n.OnFailureOf; parent != "" {
			pr := s.results[parent]
			if pr.Outcome != sparkwing.Failed {
				s.terminal(ctx, n.ID, sparkwing.Skipped, fmt.Sprintf("parent %q did not fail (outcome=%s)", parent, pr.Outcome))
				continue
			}
			stage := "action"
			if pr.Reason == store.FailureVerify {
				stage = "verify"
			}
			node.Failure = &nodeproto.Failure{Node: parent, Stage: stage, Message: errText(pr.Err)}
		} else if s.upstreamFailed(waits, byID) {
			s.terminal(ctx, n.ID, sparkwing.Cancelled, "upstream-failed")
			continue
		}
		s.started[n.ID] = true
		s.order = append(s.order, n.ID)
		*running++
		go func() { done <- finished{id: n.ID, res: s.exec(ctx, node)} }()
	}
	return progressed
}

// safety: a continue_on_error upstream's failure does not hold back its
// dependents, as in the in-process dispatcher.
func (s *scheduler) upstreamFailed(waits []string, byID map[string]nodeproto.PlanNode) bool {
	for _, d := range waits {
		if s.results[d].Outcome.OK() {
			continue
		}
		if m := byID[d].Modifiers; m == nil || !m.ContinueOnError {
			return true
		}
	}
	return false
}

func (s *scheduler) finish(ctx context.Context, id string, res Result) {
	switch {
	case res.Outcome == "":
		res.Outcome = sparkwing.Failed
		res.Err = errors.Join(res.Err, fmt.Errorf("node %s reported no outcome", id))
	case res.Outcome == sparkwing.Failed && errors.Is(ctx.Err(), context.Canceled):
		res.Outcome, res.Err = sparkwing.Cancelled, errors.New("cancelled: run failing")
	}
	writeCtx := context.WithoutCancel(ctx)
	err := s.run.State.FinishNodeWithReason(writeCtx, s.run.ID, id, string(res.Outcome), errText(res.Err), res.Output, res.Reason, nil)
	if errors.Is(err, store.ErrOutputNotStored) {
		// safety: a refused output fails the node in its row too, so the row never stays running.
		res = Result{Outcome: sparkwing.Failed, Err: err}
		err = s.run.State.FinishNodeWithReason(writeCtx, s.run.ID, id, string(res.Outcome), err.Error(), nil, store.FailureUnknown, nil)
	}
	if err != nil {
		// safety: a result the store never took must not read as a success downstream.
		s.run.Logger.Error("nodehost: record node result", "node", id, "err", err)
		res.Outcome, res.Err = sparkwing.Failed, errors.Join(res.Err, fmt.Errorf("record node result: %w", err))
	}
	s.results[id] = res
}

func (s *scheduler) terminal(ctx context.Context, id string, outcome sparkwing.Outcome, reason string) {
	s.started[id] = true
	s.finish(ctx, id, Result{Outcome: outcome, Err: errors.New(reason)})
}

func (s *scheduler) add(ctx, writeCtx context.Context, nodes []nodeproto.PlanNode) error {
	if err := validateNodes(append(slices.Clone(s.nodes), nodes...)); err != nil {
		return err
	}
	for i, n := range nodes {
		if err := s.run.State.CreateNode(writeCtx, pendingRow(s.run, n)); err != nil {
			err = fmt.Errorf("create node %s: %w", n.ID, err)
			s.nodes = append(s.nodes, nodes[:i]...)
			for _, created := range nodes[:i] {
				s.terminal(ctx, created.ID, sparkwing.Failed, err.Error())
			}
			return err
		}
	}
	s.nodes = append(s.nodes, nodes...)
	return nil
}

func pendingRow(run *Run, n nodeproto.PlanNode) store.Node {
	row := store.Node{
		RunID: run.ID, NodeID: n.ID, Status: "pending", RequestedSlots: 1,
		Deps: append(slices.Clone(n.Deps), n.OptionalDeps...),
	}
	var runsOn, whenRunner []string
	if m := n.Modifiers; m != nil {
		runsOn, whenRunner = m.RunsOn, m.WhenRunner
		row.PrefersLabels = m.Prefers
		row.RequestedCores, row.RequestedMemoryBytes = m.ResCores, m.ResMemoryBytes
	}
	row.NeedsLabels = appendNew(appendNew(slices.Clone(runsOn), run.Doc.Requires), whenRunner)
	return row
}

func appendNew(to, from []string) []string {
	for _, l := range from {
		if !slices.Contains(to, l) {
			to = append(to, l)
		}
	}
	return to
}

// CancelError is the cause to cancel a run's context with when the run is
// cancelled rather than failed: an interrupt, an eviction, a supersession or
// a daemon's cancel. Any other cause, a deadline included, fails the run.
type CancelError struct{ Reason string }

func (e *CancelError) Error() string { return e.Reason }

func runStatus(err error) string {
	var cancel *CancelError
	switch {
	case err == nil:
		return "success"
	case errors.As(err, &cancel):
		return "cancelled"
	}
	return "failed"
}

// safety: recovery and optional nodes never decide the run's status, and a
// cause other than a plain cancel or deadline decides it, as in the dispatcher.
func (s *scheduler) verdict(ctx context.Context) (string, error) {
	cause := context.Cause(ctx)
	if cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return runStatus(cause), cause
	}
	status, err := s.nodeVerdict()
	if err == nil && cause != nil {
		return "failed", cause
	}
	return status, err
}

func (s *scheduler) nodeVerdict() (string, error) {
	var failed, cancelled, superseded []string
	for _, n := range s.nodes {
		oc := s.results[n.ID].Outcome
		if oc.OK() || n.OnFailureOf != "" || (n.Modifiers != nil && n.Modifiers.Optional) {
			continue
		}
		switch oc {
		case sparkwing.Superseded:
			superseded = append(superseded, n.ID)
		case sparkwing.Cancelled:
			cancelled = append(cancelled, n.ID)
		default:
			failed = append(failed, n.ID)
		}
	}
	switch {
	case len(failed) > 0 && len(cancelled) > 0:
		return "failed", fmt.Errorf("nodes failed: %v; %d more cancelled with the run", failed, len(cancelled))
	case len(failed) > 0:
		return "failed", fmt.Errorf("nodes failed: %v", failed)
	case len(cancelled) > 0:
		return "failed", fmt.Errorf("nodes cancelled: %d node(s) stopped before completing", len(cancelled))
	case len(superseded) > 0:
		return "cancelled", fmt.Errorf("nodes superseded by a newer arrival: %v", superseded)
	}
	return "success", nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type discardSink struct{}

func (discardSink) Text(string, string)        {}
func (discardSink) Record(nodeproto.LogRecord) {}
