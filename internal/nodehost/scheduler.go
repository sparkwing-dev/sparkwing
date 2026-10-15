package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
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
	Session    SessionConfig
	Pipeline   string
	Entrypoint string
	// Args are the invocation's flag values; describe's defaults fill in
	// the flags it leaves out.
	Args map[string]string
	// Run is the run context plan code reads. RunID is required; a zero
	// StartedAt means now and an empty trigger source means manual.
	Run nodeproto.RunContext
	// Requires are the placement labels the host reads from the pipeline's
	// YAML; they join the plan's own.
	Requires []string
	// StartAt and StopAt bound the steps that run, as --sw-start-at and
	// --sw-stop-at; each must name a step in the plan.
	StartAt, StopAt string
	State           storage.StateStoreOld
	// Artifacts resolves storage under the current job's authenticated binding.
	// A job that declares artifacts requires this binding before it executes.
	Artifacts func(context.Context, *NodeRun) (artifacts.Data, error)
	Attempts  *Attempts
	Logger    *slog.Logger
	// Admission admits each node before it runs; nil admits every node at once.
	Admission *Admission
	// Debug holds the run's debug pauses.
	Debug Debug
	// Invocation is the run row's invocation record, which the read paths
	// redact secret arguments by.
	Invocation map[string]any
	// CheckArgs, when set, refuses the run before its row is recorded, given
	// the arguments the run plans with, describe's defaults filled in.
	CheckArgs    func(map[string]string) error
	CheckSecrets func(context.Context, []string) error
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
	Plan nodeproto.JobSpec
	// Failure says how the parent failed; it is set only for an on_failure node.
	Failure *nodeproto.JobFailure
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
	validation nodeproto.ValidationContext
	ID         string
	Doc        nodeproto.PlanDoc
	Secrets    []string
	Requires   []string
	// Pipeline, Args and Context are the accepted plan's request; every
	// later process of the run plans with the same.
	Pipeline   string
	Entrypoint string
	Args       map[string]string
	Context    nodeproto.RunContext
	StartAt    string
	StopAt     string
	State      storage.StateStoreOld
	Logger     *slog.Logger
	// Masker is the run's; a node's result is masked with it where it is recorded.
	Masker    *secrets.Masker
	onlySkips map[string]string
	// safety: set once, before any node starts, so node goroutines read it unlocked.
	leaseToken string
}

// Layer wraps an executor with one part of the host envelope.
type Layer func(cfg *Config, next Executor) Executor

// Execute plans the run in a short-lived SDK process, records the run and
// its nodes, and runs the nodes in dependency order. An error means no run
// row was written; once one exists, every failure is in the result.
func Execute(ctx context.Context, cfg Config) (res RunResult, err error) {
	if cfg.Entrypoint == "" {
		cfg.Entrypoint = cfg.Pipeline
	}
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
	var masker *secrets.Masker
	if cfg.Attempts != nil {
		masker = cfg.Attempts.Masker
	}
	cfg.State, cfg.Logger = maskState(cfg.State, masker), maskLogger(cfg.Logger, masker)
	finishCtx := context.WithoutCancel(ctx)
	releaseLease := func() {}
	defer func() { releaseLease() }()
	defer func() {
		if res.RunID == "" {
			return
		}
		if res.Err != nil && masker != nil {
			res.Err = maskedRunError{err: res.Err, text: masker.Mask(res.Err.Error())}
		}
		if ferr := cfg.State.FinishRun(finishCtx, res.RunID, res.Status, errText(res.Err)); ferr != nil {
			res.Status, res.Err = "failed", errors.Join(res.Err, fmt.Errorf("persist run state: %w", ferr))
		}
	}()
	planned, err := planDocument(ctx, cfg.Session, PlanRequest{
		Pipeline: cfg.Pipeline, Entrypoint: cfg.Entrypoint, Args: cfg.Args, Run: cfg.Run,
		Requires: cfg.Requires, Masker: masker, StartAt: cfg.StartAt, StopAt: cfg.StopAt,
		CheckArgs: cfg.CheckArgs, CheckSecrets: cfg.CheckSecrets,
	}, func(args map[string]string) error {
		if err := cfg.State.CreateRun(ctx, runRow(cfg, args)); err != nil {
			return err
		}
		res = RunResult{RunID: cfg.Run.RunID, Nodes: map[string]Result{}}
		return nil
	})
	if res.RunID == "" {
		return RunResult{}, err
	}
	doc, raw, args := planned.Doc, planned.JSON, planned.Args
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
	run := newRun(&cfg, doc, args, skips)
	run.Secrets = planned.Secrets
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
	if err := s.add(ctx, ctx, run.Doc.Jobs, nil); err != nil {
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

func newRun(cfg *Config, doc nodeproto.PlanDoc, args, skips map[string]string) *Run {
	if cfg.Entrypoint == "" {
		cfg.Entrypoint = cfg.Pipeline
	}
	run := &Run{
		ID: cfg.Run.RunID, Doc: doc, validation: cfg.Session.Validation, Requires: slices.Clone(cfg.Requires), Pipeline: cfg.Pipeline, Entrypoint: cfg.Entrypoint, Args: args, Context: cfg.Run,
		StartAt: cfg.StartAt, StopAt: cfg.StopAt, State: cfg.State, Logger: cfg.Logger,
		onlySkips: skips,
	}
	if cfg.Attempts != nil {
		run.Masker = cfg.Attempts.Masker
	}
	return run
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

func chain(cfg *Config) Executor {
	if cfg.Dispatch != nil {
		return dispatchChain(cfg)
	}
	return podChain(cfg, "")
}

func wrapLayers(cfg *Config, next Executor, side LayerSide) Executor {
	implementations := map[string]Layer{
		"debug": debugLayer, "skip_if": skipIfLayer, "approval": approvalsLayer,
		"memo": memoLayer, "admission": admissionLayer, "artifacts": artifactsLayer,
		"caches": cachesLayer, "retry": retryLayer, "timeout": timeoutLayer,
	}
	table := LayerTable()
	for i := len(table) - 1; i >= 0; i-- {
		if side != "" && table[i].Side != side {
			continue
		}
		wrap := implementations[table[i].Name]
		if wrap == nil {
			return func(context.Context, *NodeRun) Result { return failed(errors.New("nodehost: unsupported host layer")) }
		}
		next = wrap(cfg, next)
	}
	return next
}

// safety: one goroutine owns the maps; executors report back over a channel.
type scheduler struct {
	run     *Run
	exec    Executor
	nodes   []nodeproto.JobSpec
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
		f := <-done
		running--
		s.finish(ctx, f.id, f.res)
	}
}

func (s *scheduler) startReady(ctx context.Context, done chan<- finished, running *int) (progressed bool) {
	byID := make(map[string]nodeproto.JobSpec, len(s.nodes))
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
		if res, ok := s.rehydrated(ctx, n); ok {
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
func parentFailure(parent, reason, message string) *nodeproto.JobFailure {
	stage := "action"
	if reason == store.FailureVerify {
		stage = "verify"
	}
	return &nodeproto.JobFailure{Job: parent, Stage: stage, Error: &apierr.Error{Code: apierr.SDKError, Message: message}}
}

// safety: a continue_on_error upstream's failure does not hold back its
// dependents, as in the in-process dispatcher.
func (s *scheduler) upstreamFailed(waits []string, byID map[string]nodeproto.JobSpec) bool {
	for _, d := range waits {
		if s.results[d].Outcome.OK() {
			continue
		}
		if !byID[d].ContinueOnError {
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
	res = recordResult(ctx, s.run, id, res)
	s.results[id] = res
	s.expand(ctx, id, res)
}

func (s *scheduler) skipAll(ctx context.Context) {
	for _, n := range s.nodes {
		s.terminal(ctx, n.ID, sparkwing.Skipped, "a plan concurrency group is full under OnLimit:Skip")
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
func (s *scheduler) add(ctx, writeCtx context.Context, nodes []nodeproto.JobSpec, from *expansionFrom) error {
	if err := validateNodes(append(slices.Clone(s.nodes), nodes...), s.run.validation); err != nil {
		return err
	}
	return s.recordNodes(ctx, writeCtx, nodes, from)
}

func (s *scheduler) recordNodes(ctx, writeCtx context.Context, nodes []nodeproto.JobSpec, from *expansionFrom) error {
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
func (s *scheduler) resumedRows(ctx context.Context, nodes []nodeproto.JobSpec) (map[string]*store.Node, error) {
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
		case row.SpecHash != jobHash(n):
			return nil, &apierr.Error{Code: apierr.PlanDrift, Message: fmt.Sprintf(
				"node %s was planned as %s by an earlier host of this run and as %s now; plan code must be deterministic for a run",
				n.ID, row.SpecHash, jobHash(n))}
		}
		rows[n.ID] = row
	}
	return rows, nil
}

// safety: terminal results survive resume; the reaper returns recoverable work to pending.
func (s *scheduler) reuseRow(ctx context.Context, n nodeproto.JobSpec, row *store.Node) {
	id := n.ID
	if row.Status != "done" || row.Outcome == "" {
		return
	}
	res := Result{Outcome: sparkwing.Outcome(row.Outcome), Reason: row.FailureReason, recorded: true}
	if row.Error != "" {
		res.Err = errors.New(row.Error)
	}
	s.started[id], s.results[id] = true, res
	if generatesNodes(n, res.Outcome) {
		batches, err := storedExpansions(ctx, s.run.State, s.run.ID, n)
		if err != nil {
			for _, g := range n.Closures.Generate {
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
	outputs, ok := baseState(st).(sourceOutputs)
	if !ok {
		return nil, fmt.Errorf("read node %s output: the runs store cannot read it", id)
	}
	out, err := outputs.GetNodeOutput(ctx, runID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return out, err
}

func pendingRow(run *Run, n nodeproto.JobSpec) store.Node {
	row := store.Node{
		RunID: run.ID, NodeID: n.ID, Status: "pending", RequestedSlots: 1, SpecHash: jobHash(n),
		Deps: append(slices.Clone(n.Needs), n.NeedsIfPresent...),
	}
	row.PrefersLabels = slices.Clone(n.Prefers)
	if r := n.Resources; r != nil {
		row.RequestedCores, row.RequestedMemoryBytes = value(r.Cores), value(r.MemoryBytes)
	}
	row.NeedsLabels = appendNew(slices.Clone(n.Requires), run.Requires)
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
		if oc.OK() || n.OnFailureOf != "" || n.Optional {
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

// safety: the one writer of a node's result, for the scheduler and a pod host. A refused output fails the node in
// its row too, so the row never stays running, and a result the store never took never reads as a success. Layers
// outside the attempt, and the store itself, fail with text no attempt masked, so the result is masked after its
// last change and every text it records is masked before it is bounded.
func recordResult(ctx context.Context, run *Run, id string, res Result) Result {
	writeCtx := context.WithoutCancel(ctx)
	var err error
	if !res.recorded {
		err = run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), recordedErrText(maskErr(run.Masker, res.Err)), res.Output, res.Reason, nil)
	}
	if errors.Is(err, store.ErrOutputNotStored) {
		res = Result{Outcome: sparkwing.Failed, Err: err}
		err = run.State.FinishNodeWithReason(writeCtx, run.ID, id, string(res.Outcome), recordedErrText(maskErr(run.Masker, err)), nil, store.FailureUnknown, nil)
	}
	if err != nil {
		run.Logger.Error("nodehost: record node result", "node", id, "err", err)
		res.Outcome, res.Err = sparkwing.Failed, errors.Join(res.Err, fmt.Errorf("record node result: %w", err))
	}
	if res.Err != nil {
		res.Err = errors.New(run.Masker.Mask(res.Err.Error()))
	}
	nodeEndEvent(writeCtx, run, id, res)
	return res
}

func maskErr(m *secrets.Masker, err error) error {
	if err == nil || m == nil {
		return err
	}
	return maskHostError(err, m.Mask)
}

type discardSink struct{}

func (discardSink) Text(string, string)       {}
func (discardSink) Record(nodeproto.FrameLog) {}
