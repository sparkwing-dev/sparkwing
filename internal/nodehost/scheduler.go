package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
	Run nodeproto.RunContextOld
	// Requires are the placement labels the host reads from the pipeline's
	// YAML; they join the plan's own.
	Requires []string
	// StartAt and StopAt bound the steps that run, as --sw-start-at and
	// --sw-stop-at; each must name a step in the plan.
	StartAt, StopAt string
	State           storage.StateStoreOld
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
	// Debug holds the run's debug pauses.
	Debug Debug
	// Invocation is the run row's invocation record, which the read paths
	// redact secret arguments by.
	Invocation map[string]any
	// CheckArgs, when set, refuses the run before its row is recorded, given
	// the arguments the run plans with, describe's defaults filled in.
	CheckArgs func(map[string]string) error
	// Memo reuses memoized nodes' earlier results; nil runs every node.
	Memo *Memo
	// Dispatch, when set, runs each admitted node elsewhere, whole: the
	// remote host runs its artifacts, caches, retries, timeouts and attempts
	// and records its start and result. Debug pauses, skip_if, approvals,
	// memo and admission stay in this host, with Attempts for their eval
	// processes and logs. Nil runs every node in this process.
	Dispatch Executor
	// ParentRunID, RetryOf and RetrySource are the run row's lineage.
	ParentRunID, RetryOf, RetrySource string
	// Resume reuses the node rows an earlier host of this run left, as a
	// trigger reclaimed after its runner stopped has: a finished node keeps
	// its result, and an unfinished one, or one whose holder was lost, runs
	// again. A row planned under another spec_hash fails the run with
	// plan_drift. It needs Dispatch, whose claims keep a node from running
	// twice.
	Resume bool
	// ClaimedRowPoll is how often a dispatched node's row is read while
	// another holder claims it; zero means 5 seconds.
	ClaimedRowPoll time.Duration
	// Only, as --sw-only, runs only the plan's nodes whose ID matches the
	// glob and the nodes they depend on, skipping the rest.
	Only string
	// Rehydrate, when set, copies the results of a retried run's nodes
	// instead of running them; nil runs every node.
	Rehydrate *Rehydration
}

// Result is how one node ended.
type Result struct {
	Outcome sparkwing.Outcome
	Err     error
	// Reason is the store's failure reason, such as [store.FailureVerify].
	Reason string
	Output json.RawMessage
	// Final ends the node with this result whatever retries remain, as a
	// failed before_run does: a later attempt would run the body without it.
	Final      bool
	expansions []expansion
	// safety: the memo layer writes the row before its release records the entry, and a dispatched node's
	// remote host writes its own, so finish must not write it again.
	recorded bool
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
	// Retried reports whether the retries layer starts another attempt after
	// one that ended with res; nil means none follows. The attempt runs its
	// after_run closures only when none does.
	Retried func(res Result) bool
	// Generators are the closures that added the node, outermost first: the
	// one that added it last, after any that added its source. Empty for a
	// node of the accepted plan.
	Generators []string
	// Progress, when set, is called for each log line or record the attempt
	// stores; the no-progress timeout restarts its count on it.
	Progress func()
	// Pause, when set, stops the no-progress count until the returned resume
	// is called, which restarts it in full. A wait the node makes on the host,
	// such as a child-run await, holds it.
	Pause func() (resume func())
	// Hold, when set, stops the wall timeout until the returned resume is
	// called, which restarts what was left of it. A child run of the node
	// waiting in plan admission holds it.
	Hold func() (resume func())
	// safety: set only for a memoized node that did not run, whose generators read it through the output route.
	memoOutput json.RawMessage
}

// Run is the hosted run an executor works within.
type Run struct {
	ID string
	// Doc is the accepted plan. Nodes added with [Run.Add] are not in it.
	Doc nodeproto.PlanDocOld
	// Pipeline, Args and Context are the accepted plan's request; every
	// later process of the run plans with the same.
	Pipeline  string
	Args      map[string]string
	Context   nodeproto.RunContextOld
	StartAt   string
	StopAt    string
	State     storage.StateStoreOld
	Logger    *slog.Logger
	adds      chan addRequest
	onlySkips map[string]string
	// safety: set once, before any node starts, so node goroutines read it unlocked.
	leaseToken string
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

// safety: the order is the envelope's nesting, outermost first: debug pauses, then skip_if, which the
// dispatcher checks after a pause-before but before a gate waits, then approvals and recovery, memo, which
// copies a hit before any admission lease or group slot is taken, admission, artifacts, caches,
// retries and timeouts, expansion. The attempt closures and steps run inside the attempt executor.
var layers = []Layer{
	debugLayer,
	skipIfLayer,
	approvalsLayer,
	memoLayer,
	admissionLayer,
	artifactsLayer,
	cachesLayer,
	retryLayer,
	timeoutLayer,
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
	if cfg.Resume && cfg.Dispatch == nil {
		return RunResult{}, errors.New("nodehost: Resume needs Dispatch, whose claims keep a resumed node from running twice")
	}
	if cfg.Run.StartedAt.IsZero() {
		cfg.Run.StartedAt = time.Now().UTC()
	}
	if cfg.Run.Trigger == nil {
		cfg.Run.Trigger = &nodeproto.Trigger{Source: "manual"}
	}
	scfg := planSession(cfg)
	scfg.HoldOutput = true
	sess, err := StartSession(ctx, scfg)
	if err != nil {
		return RunResult{}, err
	}
	closeSession := func() {
		if _, err := sess.Close(); err != nil {
			cfg.Logger.Warn("nodehost: plan process", "err", err)
		}
	}
	var masker *secrets.Masker
	if cfg.Attempts != nil {
		masker = cfg.Attempts.Masker
	}
	args, known, err := describeArgs(ctx, sess, cfg.Pipeline, cfg.Args, masker)
	if err == nil && cfg.CheckArgs != nil {
		err = cfg.CheckArgs(args)
	}
	err = releaseDescribed(sess, known, masker, err)
	if err == nil {
		err = cfg.State.CreateRun(ctx, runRow(cfg, args))
	}
	if err != nil {
		closeSession()
		return RunResult{}, err
	}

	res = RunResult{RunID: cfg.Run.RunID, Nodes: map[string]Result{}}
	finishCtx := context.WithoutCancel(ctx)
	// safety: registered before FinishRun's defer so the run lease ends only
	// once the run row is terminal; the daemon finalizes a run whose lease
	// ends while its row still runs as orphaned.
	releaseLease := func() {}
	defer func() { releaseLease() }()
	// safety: registered before the heartbeat's defer so the heartbeat has
	// stopped before the run row goes terminal.
	defer func() {
		// safety: plan code's error can quote a secret flag, and the run row and
		// every caller that prints the error outlive the masker.
		if res.Err != nil && masker != nil {
			res.Err = maskedRunError{err: res.Err, text: masker.Mask(res.Err.Error())}
		}
		if ferr := cfg.State.FinishRun(finishCtx, res.RunID, res.Status, errText(res.Err)); ferr != nil {
			res.Status, res.Err = "failed", errors.Join(res.Err, fmt.Errorf("persist run state: %w", ferr))
		}
	}()
	stopHeartbeat := heartbeat(ctx, cfg, res.RunID)
	defer stopHeartbeat()

	doc, raw, err := planRun(ctx, sess, cfg.Pipeline, args, cfg.Run)
	if err == nil {
		doc, raw, err = hostRequires(doc, raw, cfg.Requires)
	}
	closeSession()
	if err == nil {
		err = validateStepRange(doc, cfg.StartAt, cfg.StopAt)
	}
	var skips map[string]string
	if err == nil {
		skips, err = onlySkips(doc, cfg.Only, cfg.StartAt, cfg.StopAt)
	}
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
		StartAt: cfg.StartAt, StopAt: cfg.StopAt, State: cfg.State, Logger: cfg.Logger, adds: make(chan addRequest),
		onlySkips: skips,
	}
	releaseLease = runPlanned(ctx, &cfg, run, &res)
	return res, nil
}

// safety: the run lease outlives runPlanned; its caller calls release once the
// run row is terminal.
func runPlanned(ctx context.Context, cfg *Config, run *Run, res *RunResult) (release func()) {
	release = func() {}
	s := &scheduler{run: run, results: res.Nodes, started: map[string]bool{}, exec: chain(cfg), resume: cfg.Resume}
	s.rehydrate, s.rerun = cfg.Rehydrate, map[string]bool{}
	if cfg.Attempts != nil {
		s.regenerate = func(ctx context.Context, n *NodeRun, output json.RawMessage) []expansion {
			return memoGenerate(ctx, cfg, n, output)
		}
	}
	if err := s.add(ctx, ctx, run.Doc.Nodes, nil); err != nil {
		res.Status, res.Err = "failed", err
		return release
	}
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	body := func(ctx context.Context) runEnd {
		s.loop(ctx)
		status, err := s.verdict(ctx)
		return runEnd{status: status, err: err}
	}
	var end runEnd
	if cfg.Admission == nil {
		end = body(runCtx)
	} else {
		end, release = cfg.Admission.hostRun(runCtx, run, cancelRun, body)
	}
	if end.skipped {
		s.skipAll(ctx)
		end.status, end.err = s.nodeVerdict()
	} else if len(s.started) < len(s.nodes) {
		// safety: a run refused before its first node leaves no node row pending.
		s.cancelAll(ctx, end.err)
	}
	res.Order = s.order
	res.Status, res.Err = end.status, end.err
	return release
}

func runRow(cfg Config, args map[string]string) store.Run {
	rc := cfg.Run
	row := store.Run{
		ID: rc.RunID, Pipeline: cfg.Pipeline, Status: "running", Args: args,
		TriggerSource: rc.Trigger.Source, StartedAt: rc.StartedAt, Invocation: cfg.Invocation,
		ParentRunID: cfg.ParentRunID, RetryOf: cfg.RetryOf, RetrySource: cfg.RetrySource,
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
	if cfg.Dispatch != nil {
		return dispatchChain(cfg)
	}
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
		if n.Attempt <= 1 {
			hostEvent(ctx, n.Run, n.Plan.ID, "node_started", nil)
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
	groups  map[string]*genGroup
	addedBy map[string]string
	// safety: the node whose generator added each generated node, so a node's generator chain reaches the plan.
	sourceOf map[string]string
	resume   bool
	// safety: rerun records the nodes a retry runs again, so a node waiting on its deps is looked up in the
	// source run once; only the scheduler goroutine reads it.
	rehydrate  *Rehydration
	rerun      map[string]bool
	adopted    []finished
	regenerate func(ctx context.Context, n *NodeRun, output json.RawMessage) []expansion
	drifts     []error
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
			req.reply <- s.add(ctx, req.ctx, req.nodes, nil)
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
		if reason, ok := s.run.onlySkips[n.ID]; ok {
			s.terminal(ctx, n.ID, sparkwing.Skipped, reason)
			progressed = true
			continue
		}
		waits, generated, expErr := s.generatedWaits(n, waitsOn(n, byID))
		if expErr != nil {
			s.terminal(ctx, n.ID, sparkwing.Cancelled, fmt.Sprintf("expansion failed: %v", expErr))
			progressed = true
			continue
		}
		if !generated || slices.ContainsFunc(waits, func(id string) bool { _, ok := s.results[id]; return !ok }) {
			continue
		}
		progressed = true
		if res, ok := s.rehydrated(ctx, n, byID); ok {
			s.started[n.ID] = true
			s.finish(ctx, n.ID, res)
			continue
		}
		node := &NodeRun{Run: s.run, Plan: n, Generators: s.generators(n.ID)}
		if parent := n.OnFailureOf; parent != "" {
			pr := s.results[parent]
			if pr.Outcome != sparkwing.Failed {
				s.terminal(ctx, n.ID, sparkwing.Skipped, fmt.Sprintf("parent %q did not fail (outcome=%s)", parent, pr.Outcome))
				continue
			}
			node.Failure = parentFailure(parent, pr.Reason, errText(pr.Err))
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

// safety: one rule for the run host and a dispatched recovery node's host,
// which reads its parent's reason and error from the row this host recorded.
func parentFailure(parent, reason, message string) *nodeproto.Failure {
	stage := "action"
	if reason == store.FailureVerify {
		stage = "verify"
	}
	return &nodeproto.Failure{Node: parent, Stage: stage, Message: message}
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
	var err error
	if !res.recorded {
		err = s.run.State.FinishNodeWithReason(writeCtx, s.run.ID, id, string(res.Outcome), recordedErrText(res.Err), res.Output, res.Reason, nil)
	}
	if errors.Is(err, store.ErrOutputNotStored) {
		// safety: a refused output fails the node in its row too, so the row never stays running.
		res = Result{Outcome: sparkwing.Failed, Err: err}
		err = s.run.State.FinishNodeWithReason(writeCtx, s.run.ID, id, string(res.Outcome), recordedErrText(err), nil, store.FailureUnknown, nil)
	}
	if err != nil {
		// safety: a result the store never took must not read as a success downstream.
		s.run.Logger.Error("nodehost: record node result", "node", id, "err", err)
		res.Outcome, res.Err = sparkwing.Failed, errors.Join(res.Err, fmt.Errorf("record node result: %w", err))
	}
	nodeEndEvent(writeCtx, s.run, id, res)
	s.results[id] = res
	s.expand(ctx, id, res)
}

func (s *scheduler) skipAll(ctx context.Context) {
	for _, n := range s.nodes {
		s.terminal(ctx, n.ID, sparkwing.SkippedConcurrent, "a plan concurrency group is full under OnLimit:Skip")
	}
}

func (s *scheduler) cancelAll(ctx context.Context, cause error) {
	for _, n := range s.nodes {
		if !s.started[n.ID] {
			s.terminal(ctx, n.ID, sparkwing.Cancelled, "run not admitted: "+errText(cause))
		}
	}
}

func (s *scheduler) terminal(ctx context.Context, id string, outcome sparkwing.Outcome, reason string) {
	s.started[id] = true
	s.finish(ctx, id, Result{Outcome: outcome, Err: errors.New(reason)})
}

// safety: from, when set, names the source and generator that returned nodes, and each row records it with the
// node's whole spec, so no later host of the run generates the node again.
func (s *scheduler) add(ctx, writeCtx context.Context, nodes []nodeproto.PlanNode, from *expansionFrom) error {
	if err := validateNodes(append(slices.Clone(s.nodes), nodes...)); err != nil {
		return err
	}
	existing, err := s.resumedRows(writeCtx, nodes)
	if err != nil {
		return err
	}
	for i, n := range nodes {
		if row, ok := existing[n.ID]; ok {
			s.reuseRow(writeCtx, n, row)
			continue
		}
		row := pendingRow(s.run, n)
		err := from.record(&row, n, len(nodes))
		if err == nil {
			err = s.run.State.CreateNode(writeCtx, row)
		}
		if err != nil {
			err = fmt.Errorf("create node %s: %w", n.ID, err)
			s.nodes = append(s.nodes, nodes[:i]...)
			// safety: only rows this host created are failed; a reused row is its holder's or its run's record.
			for _, prior := range nodes[:i] {
				if _, reused := existing[prior.ID]; !reused {
					s.terminal(ctx, prior.ID, sparkwing.Failed, err.Error())
				} else {
					s.started[prior.ID] = true
				}
			}
			return err
		}
	}
	s.nodes = append(s.nodes, nodes...)
	// safety: an adopted generator's nodes are added only once it is among the run's nodes, which they need.
	for len(s.adopted) > 0 {
		next := s.adopted[0]
		s.adopted = s.adopted[1:]
		s.expand(ctx, next.id, next.res)
	}
	return nil
}

// safety: a resumed run reads every row an earlier host left before it
// writes any, so a node planned under another spec_hash refuses the whole
// resume with plan_drift and no row, a live holder's included, is touched.
func (s *scheduler) resumedRows(ctx context.Context, nodes []nodeproto.PlanNode) (map[string]*store.Node, error) {
	if !s.resume {
		return nil, nil
	}
	rows := map[string]*store.Node{}
	for _, n := range nodes {
		row, err := s.run.State.GetNode(ctx, s.run.ID, n.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("read node %s: %w", n.ID, err)
		case row.SpecHash != n.SpecHash:
			return nil, &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
				"node %s was planned as %s by an earlier host of this run and as %s now; plan code must be deterministic for a run",
				n.ID, row.SpecHash, n.SpecHash)}
		}
		rows[n.ID] = row
	}
	return rows, nil
}

// safety: a resumed run's finished node keeps the result its row records and
// is never run or written again; an unfinished row, or one the reaper failed
// because its holder was lost, is run again.
func (s *scheduler) reuseRow(ctx context.Context, n nodeproto.PlanNode, row *store.Node) {
	id := n.ID
	if row.Status != "done" || row.Outcome == "" {
		return
	}
	if row.FailureReason == store.FailureAgentLost {
		if r, ok := s.run.State.(lostNodeResetter); ok && r.ResetNodeForAutoRetry(ctx, s.run.ID, id) == nil {
			return
		}
	}
	res := Result{Outcome: sparkwing.Outcome(row.Outcome), Reason: row.FailureReason, recorded: true}
	if row.Error != "" {
		res.Err = errors.New(row.Error)
	}
	s.started[id], s.results[id] = true, res
	if generatesNodes(n, res.Outcome) {
		batches, err := storedExpansions(ctx, s.run.State, s.run.ID, n)
		if err != nil {
			for _, g := range n.Closures.Generator {
				res.expansions = append(res.expansions, expansion{closure: g, err: err})
			}
		} else {
			res.expansions = s.recordedExpansions(ctx, n, batches, func() (json.RawMessage, error) {
				return recordedOutput(ctx, s.run.State, s.run.ID, id)
			})
		}
		s.results[id] = res
		s.adopted = append(s.adopted, finished{id: id, res: res})
	}
}

func recordedOutput(ctx context.Context, st storage.StateStoreOld, runID, id string) (json.RawMessage, error) {
	outputs, ok := st.(sourceOutputs)
	if !ok {
		return nil, fmt.Errorf("read node %s output: the runs store cannot read it", id)
	}
	out, err := outputs.GetNodeOutput(ctx, runID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return out, err
}

// safety: the store reopens a node whose holder was lost only for the run's own trigger holder, and only while no
// agent-loss retry run took the run over, so the node never runs in both.
type lostNodeResetter interface {
	ResetNodeForAutoRetry(ctx context.Context, runID, nodeID string) error
}

func pendingRow(run *Run, n nodeproto.PlanNode) store.Node {
	row := store.Node{
		RunID: run.ID, NodeID: n.ID, Status: "pending", RequestedSlots: 1, SpecHash: n.SpecHash,
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

func runStatus(err error) string {
	var cancel *lease.CancelError
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
	if len(s.drifts) > 0 {
		return "failed", fmt.Errorf("generated nodes drifted from what the run recorded: %w", errors.Join(s.drifts...))
	}
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

type discardSink struct{}

func (discardSink) Text(string, string)        {}
func (discardSink) Record(nodeproto.LogRecord) {}
