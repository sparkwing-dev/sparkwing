package orchestrator

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// HostedEntry is the pipeline program a hosted run starts each SDK process
// from, and what the CLI knows about the invocation that the run records.
type HostedEntry struct {
	// Command is run with --sw-node-protocol appended in Dir, the repository
	// root, with Env, the user's environment.
	Command pipelineentry.Command
	Dir     string
	Env     []string
	// SecretArgs names the pipeline's secret flags, whose values are masked
	// and redacted from the run row.
	SecretArgs       []string
	PipelineRevision string
	BinarySource     string
	// Flags are the invocation flags only the CLI knows, such as ref and allow.
	Flags map[string]any
}

// RunHosted runs opts.Pipeline on this machine through the engine host: every
// node runs in its own SDK process of entry.Command, and this process owns
// the DAG, the store writes and the envelope.
func RunHosted(ctx context.Context, paths Paths, opts Options, entry HostedEntry) (res *Result, err error) {
	// safety: a sparkwing run started inside a dry-run node is a dry run too, as the dispatcher read it.
	opts.DryRun = opts.DryRun || dryRunFromEnv()
	if err := applyHostedProjectConfig(&opts, entry.Dir); err != nil {
		return nil, err
	}
	backends, closeBackends, err := localRunBackends(ctx, paths, &opts)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeBackends()) }()
	invokeArgs := mergeInvokeArgs(opts)
	var checkArgs func(map[string]string) error
	if opts.PipelineYAML != nil {
		at := pipelines.GuardContext{ProfileIsLocal: backends.LocalCoordination}
		if opts.Git != nil {
			at.GitBranch, at.GitDefaultBranch = opts.Git.Branch, opts.Git.DefaultBranch
		}
		checkArgs = guardCheck(opts.Pipeline, opts.PipelineYAML.Guards, at, func() string { return "" })
	}
	if opts.RunID == "" {
		opts.RunID = newRunID()
	}
	runID := opts.RunID
	if err := paths.EnsureRunDir(runID); err != nil {
		return nil, fmt.Errorf("ensure run dir: %w", err)
	}
	if envLog, envErr := newEnvelopeLogger(paths.EnvelopeLog(runID), opts.Delegate); envErr == nil {
		opts.Delegate = envLog
		defer func() { warnHostedClose("envelope log", envLog.Close()) }()
	}
	ctx, stopSignals := hostedInterruptCancel(ctx)
	defer stopSignals()
	if opts.priority, opts.prioritySet, err = resolveRunPriority(ctx, opts.Priority, opts.Admission); err != nil {
		return nil, err
	}

	// safety: nodehost registers the secret-flagged args with this masker while it plans, before any node runs.
	masker := secrets.NewMasker()
	opts.Delegate = nodehost.MaskingLogger(opts.Delegate, masker)
	routes, err := nodehost.Start(slog.Default())
	if err != nil {
		return nil, err
	}
	//nolint:contextcheck // Close bounds its own shutdown; the run's context may already be cancelled here.
	defer func() { warnHostedClose("node routes", routes.Close()) }()
	state := &hostedRunState{
		StateBackend: backends.State, logs: backends.Logs, delegate: opts.Delegate, diskRoot: backends.DiskRoot, starts: map[string]time.Time{},
		lineage:       store.Run{ParentRunID: opts.ParentRunID, RetryOf: opts.RetryOf, RetrySource: opts.RetrySource, TriggerSource: opts.Trigger.Source},
		retryPlanHash: opts.RetryPlanHash,
	}
	admission, err := hostedAdmission(ctx, &opts, backends, state.planDoc)
	if err != nil {
		return nil, err
	}

	logDir := localRunLogDir(backends.Logs, runID)
	invocation := buildRunInvocation(opts, runID, logDir, entry.SecretArgs)
	invocation["cwd"] = entry.Dir
	if entry.PipelineRevision != "" {
		invocation["pipeline_revision"] = entry.PipelineRevision
	}
	if entry.BinarySource != "" {
		invocation["binary_source"] = entry.BinarySource
	}
	if len(entry.Flags) > 0 {
		flags, _ := invocation["flags"].(map[string]any)
		if flags == nil {
			flags = map[string]any{}
		}
		maps.Copy(flags, entry.Flags)
		invocation["flags"] = flags
		invocation["reproducer"] = reproducerFor(opts.Pipeline, flags, opts.Args)
	}
	if opts.RunHandlePath != "" {
		release, err := ReserveRunHandle(opts.RunHandlePath)
		if err != nil {
			return nil, fmt.Errorf("reserve run handle: %w", err)
		}
		defer release()
		state.publishHandle = func() error {
			return PublishRunHandle(opts.RunHandlePath, NewRunHandle(runID, opts.Pipeline, logDir, "running"))
		}
	}
	emitRunStart(opts.Delegate, invocation, backends.DiskRoot)
	opts.standalone.announce()

	resolver := secrets.NewCached(localSecretsFor(ctx, paths, backends, runID, opts.Pipeline), masker)
	opts.localSecrets = true
	state.preflight = func(ctx context.Context, doc nodeproto.PlanDoc) error {
		return hostedSecretPreflight(ctx, resolver, doc, opts)
	}
	children := hostedChildren{
		state: backends.State, concurrency: backends.Concurrency, admission: opts.Admission, masker: masker, dryRun: opts.DryRun,
	}
	attempts := &nodehost.Attempts{
		Routes: routes, Masker: masker, Resolver: resolver,
		Local: backends.State, Children: children,
		OpenLog: func(ctx context.Context, runID, nodeID string) (nodehost.NodeLog, error) {
			log, err := backends.Logs.OpenNodeLog(ctx, runID, nodeID, nil)
			if err != nil {
				return nil, err
			}
			return hostedNodeLog{NodeLog: log, delegate: opts.Delegate}, nil
		},
	}
	if backends.LocalCoordination {
		attempts.Usage = nodehost.RecordUsage(backends.State)
	}
	requires, err := PipelineRequires(entry.Dir, opts.Pipeline)
	if err != nil {
		return nil, err
	}
	planSink, err := newHostedPlanSink(ctx, runID, masker, opts.Delegate)
	if err != nil {
		return nil, err
	}
	cfg := nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: entry.Command, Dir: entry.Dir, Env: engineenv.Strip(entry.Env), Sink: planSink},
		Pipeline: opts.Pipeline, Args: invokeArgs, CheckArgs: checkArgs, Run: hostedRunContext(opts, runID),
		StartAt: opts.StartAt, StopAt: opts.StopAt, Only: opts.Only, Workers: opts.MaxParallel,
		State: state, Artifacts: backends.Artifact, Attempts: attempts, Admission: admission,
		Debug: nodehost.Debug{
			PauseBefore: opts.Debug.PauseBefore, PauseAfter: opts.Debug.PauseAfter,
			PauseOnFailure: opts.Debug.PauseOnFailure, PauseTimeout: pauseTimeout(), Bounces: hostedBounces(backends.State),
		},
		Invocation: invocation, Requires: requires,
		Memo: &nodehost.Memo{
			Slots: backends.Concurrency, NoCache: opts.NoCache,
			Source: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) (nodehost.MemoSource, error) {
				return nodehost.ReadMemoSource(ctx, backends.State, runID, nodeID)
			},
		},
	}
	if cfg.Rehydrate, err = hostedRehydration(ctx, backends.State, opts, runID); err != nil {
		return nil, err
	}
	if backends.LocalCoordination {
		stopTriggers, err := hostedTriggerLoop(ctx, backends.State, runID, entry.Dir, opts.standalone)
		if err != nil {
			return nil, err
		}
		defer stopTriggers()
	}
	execStart := time.Now()
	out, err := nodehost.Execute(ctx, cfg)
	if err != nil {
		return nil, err
	}
	finishCtx := context.WithoutCancel(ctx)
	if doc := state.planDoc(); backends.LocalCoordination && doc != nil {
		var pin *capacity.Pin
		charge := runCharge{}
		if r := doc.Resources; r != nil && (r.Cores > 0 || r.MemoryBytes > 0) {
			pin = &capacity.Pin{Cores: r.Cores, MemoryBytes: r.MemoryBytes}
			charge = runCharge{Cores: r.Cores, MemoryBytes: r.MemoryBytes}
		}
		recordRunProfile(finishCtx, backends.State, currentProfileKey(opts.Pipeline), runID, pin, hostedCapacityFingerprint(doc), charge, false, execStart, time.Now())
	}
	emitHostedRunEnd(finishCtx, opts.Delegate, backends.State, out, execStart)
	return &Result{RunID: runID, Status: out.Status, Error: out.Err}, nil
}

// safety: the host reads a cancel as cancelled only from its CancelError, and
// the dispatcher recorded an interrupted run as cancelled.
func hostedInterruptCancel(parent context.Context) (context.Context, func()) {
	interrupted, stop := withInterruptCancel(parent)
	ctx, cancel := context.WithCancelCause(parent)
	stopAfter := context.AfterFunc(interrupted, func() {
		if cause := context.Cause(interrupted); cause != nil && parent.Err() == nil {
			cancel(&nodehost.CancelError{Reason: cause.Error()})
		}
	})
	return ctx, func() { stopAfter(); stop(); cancel(nil) }
}

// safety: the execution checkout's config is the one the old binary read from
// its own working directory, so a --sw-ref or declared-source run gets that
// checkout's defaults and guards rather than the caller's.
func applyHostedProjectConfig(opts *Options, dir string) error {
	_, cfg, err := projectconfig.Discover(dir)
	if err != nil {
		return &projectConfigError{err: err}
	}
	if cfg == nil {
		return nil
	}
	opts.DefaultArgs = cfg.Defaults.Args
	opts.PipelineYAML = (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(opts.Pipeline)
	if p := opts.PipelineYAML; p != nil {
		if p.Guards.IsEmpty() {
			p.Guards = cfg.Defaults.Guards
		}
	}
	return RefusePinnedLocalRun(opts.PipelineYAML)
}

func hostedRunContext(opts Options, runID string) nodeproto.RunContext {
	rc := nodeproto.RunContext{
		RunID: runID, NoCache: opts.NoCache, DryRun: opts.DryRun,
		Trigger: &nodeproto.Trigger{Source: protocolTriggerSource(opts.Trigger.Source), User: opts.Trigger.User},
	}
	if g := opts.Git; g != nil {
		rc.Git = &nodeproto.Git{SHA: g.SHA, Branch: g.Branch, DefaultBranch: g.DefaultBranch, Repo: g.Repo, RepoURL: g.RepoURL}
	}
	return rc
}

func hostedBounces(state StateBackend) nodehost.Bounces {
	switch s := state.(type) {
	case localState:
		return s.st
	case *client.Client:
		return s
	}
	return nil
}

// safety: plan code's logs have no node log to land in, so they go to the
// renderer through a node-host log sink, which masks its records as a node's.
func newHostedPlanSink(ctx context.Context, runID string, masker *secrets.Masker, delegate sparkwing.Logger) (*nodehost.LogSink, error) {
	return nodehost.NewLogSink(ctx, nodehost.LogSinkConfig{
		RunID: runID, NodeID: store.PlanNodeID, Log: hostedPlanLog{delegate}, Masker: masker,
	})
}

// safety: plan output belongs to the run, not to a node the renderer would
// look for, so the sink's plan node id is dropped.
type hostedPlanLog struct{ delegate sparkwing.Logger }

func (l hostedPlanLog) Emit(rec sparkwing.LogRecord) {
	if l.delegate == nil {
		return
	}
	rec.JobID = ""
	l.delegate.Emit(rec)
}

// hack: the host has no event seam yet, so the run_plan, node_start and node_end records the dispatcher wrote
// come from the state writes that mark those moments.
type hostedRunState struct {
	StateBackend
	logs     LogBackend
	delegate sparkwing.Logger
	diskRoot string

	preflight func(context.Context, nodeproto.PlanDoc) error
	// safety: the handle is published only once its run row exists, so it never names a run the store lacks.
	publishHandle func() error
	// safety: the run row records the surface that started the run, such as a child's await-pipeline, which
	// the node protocol's run context names only by the protocol source it means.
	lineage store.Run
	// safety: a retry copies its source run's results only when it planned the same plan.
	retryPlanHash string

	mu     sync.Mutex
	doc    *nodeproto.PlanDoc
	starts map[string]time.Time
}

func (s *hostedRunState) CreateRun(ctx context.Context, r store.Run) error {
	r.ParentRunID, r.RetryOf, r.RetrySource = s.lineage.ParentRunID, s.lineage.RetryOf, s.lineage.RetrySource
	r.TriggerSource = cmp.Or(s.lineage.TriggerSource, r.TriggerSource)
	if err := s.StateBackend.CreateRun(ctx, r); err != nil {
		return err
	}
	if s.publishHandle == nil {
		return nil
	}
	if err := s.publishHandle(); err != nil {
		msg := fmt.Sprintf("publish run handle: %v", err)
		// safety: the run never starts, so its row must not stay running.
		if ferr := s.StateBackend.FinishRun(context.WithoutCancel(ctx), r.ID, "failed", msg); ferr != nil {
			noteLostStateWrite(context.WithoutCancel(ctx), "finish run", r.ID, ferr)
		}
		return errors.New(msg)
	}
	return nil
}

func (s *hostedRunState) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	if err := retryPlanMatches(snapshot, runID, s.lineage.RetryOf, s.retryPlanHash); err != nil {
		return err
	}
	if err := s.StateBackend.UpdatePlanSnapshot(ctx, runID, snapshot); err != nil {
		return err
	}
	var doc nodeproto.PlanDoc
	if json.Unmarshal(snapshot, &doc) == nil {
		s.mu.Lock()
		s.doc = &doc
		s.mu.Unlock()
		emitHostedPlan(s.delegate, doc)
		if s.preflight != nil {
			return s.preflight(ctx, doc)
		}
	}
	return nil
}

// safety: a run missing a required secret stops before any node starts, naming each one with its fix, as the
// dispatcher's preflight did; the snapshot write is the first point the host knows the declared secrets.
func hostedSecretPreflight(ctx context.Context, r *secrets.Cached, doc nodeproto.PlanDoc, opts Options) error {
	missing := &sparkwingruntime.MissingSecretsError{Pipeline: opts.Pipeline}
	for _, sec := range doc.Secrets {
		if sec.Optional {
			continue
		}
		if _, _, err := r.Resolve(ctx, sec.Name); errors.Is(err, secrets.ErrSecretMissing) {
			missing.Names = append(missing.Names, sec.Name)
		} else if err != nil {
			return err
		}
	}
	if len(missing.Names) == 0 {
		return nil
	}
	return secretPreflightError(missing, opts)
}

func (s *hostedRunState) planDoc() *nodeproto.PlanDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc
}

func (s *hostedRunState) StartNode(ctx context.Context, runID, nodeID string) error {
	if err := s.StateBackend.StartNode(ctx, runID, nodeID); err != nil {
		return err
	}
	// safety: each attempt starts the node again, and the dispatcher wrote one
	// node_start for the node, timed from its first attempt.
	now := time.Now()
	s.mu.Lock()
	_, again := s.starts[nodeID]
	if !again {
		s.starts[nodeID] = now
	}
	s.mu.Unlock()
	if !again {
		s.nodeRecord(ctx, runID, nodeID, func(log sparkwing.Logger) { emitNodeStart(log, now, s.diskRoot, nil) })
	}
	return nil
}

func (s *hostedRunState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	err := s.StateBackend.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
	s.mu.Lock()
	start, ok := s.starts[nodeID]
	s.mu.Unlock()
	attrs := map[string]any{"outcome": outcome}
	if ok {
		attrs["duration_ms"] = time.Since(start).Milliseconds()
	}
	if errMsg != "" {
		attrs["error"] = errMsg
	}
	if outcome == string(sparkwing.Failed) {
		s.failureExcerpt(ctx, runID, nodeID)
	}
	// safety: the memo layer writes a cached node's node_end itself, with its cache_hit attribute.
	if outcome != string(sparkwing.Cached) {
		s.nodeRecord(ctx, runID, nodeID, func(log sparkwing.Logger) {
			log.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "node_end", Attrs: attrs})
		})
	}
	return err
}

// safety: the SDK returns a failure as text, so the excerpt comes from the tail of the node log, which the
// log sink has already masked, rather than from a command's captured output.
func (s *hostedRunState) failureExcerpt(ctx context.Context, runID, nodeID string) {
	logs, ok := s.logs.(localLogs)
	if !ok {
		return
	}
	data, err := os.ReadFile(logs.paths.NodeLog(runID, nodeID))
	if err != nil {
		return
	}
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		var rec sparkwing.LogRecord
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Event == "" && rec.Msg != "" {
			lines = append(lines, rec.Msg)
		}
	}
	tail, truncated := failureExcerptTail(strings.Join(lines, "\n"), failureExcerptMaxLines, failureExcerptMaxBytes)
	if tail == "" {
		return
	}
	if payload, err := json.Marshal(failureExcerpt{LogExcerpt: tail, Truncated: truncated}); err == nil {
		noteEvent(ctx, s.StateBackend, runID, nodeID, nodeFailureExcerptEvent, payload)
	}
}

// safety: the node log is opened per record because the attempts append to
// the same file between them.
func (s *hostedRunState) nodeRecord(ctx context.Context, runID, nodeID string, write func(sparkwing.Logger)) {
	nlog, err := s.logs.OpenNodeLog(ctx, runID, nodeID, s.delegate)
	if err != nil {
		slog.Default().Warn("open node log", "run", runID, "node", nodeID, "err", err)
		return
	}
	write(nlog)
	warnHostedClose("node log", nlog.Close())
}

func warnHostedClose(what string, err error) {
	if err != nil {
		slog.Default().Warn("close "+what, "err", err)
	}
}

func emitHostedPlan(delegate sparkwing.Logger, doc nodeproto.PlanDoc) {
	rows := make([]any, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		row := map[string]any{"id": n.ID, "deps": n.Deps}
		if n.Approval != nil {
			row["approval"] = true
		}
		if len(n.Groups) > 0 {
			row["groups"] = n.Groups
		}
		if w := n.Work; w != nil && !(len(w.Steps) == 1 && w.Steps[0].ID == "run") {
			steps := make([]map[string]any, 0, len(w.Steps))
			for _, st := range w.Steps {
				steps = append(steps, map[string]any{"id": st.ID, "deps": st.Needs})
			}
			row["steps"] = steps
		}
		rows = append(rows, row)
	}
	delegate.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "run_plan", Attrs: map[string]any{"nodes": rows}})
}

func emitHostedRunEnd(ctx context.Context, delegate sparkwing.Logger, state StateBackend, out nodehost.RunResult, start time.Time) {
	if delegate == nil {
		return
	}
	rows := []any{}
	if nodes, err := state.ListNodes(ctx, out.RunID); err == nil {
		for _, n := range nodes {
			row := map[string]any{"id": n.NodeID, "outcome": n.Outcome}
			if n.StartedAt != nil && n.FinishedAt != nil {
				row["duration_ms"] = n.FinishedAt.Sub(*n.StartedAt).Milliseconds()
			}
			if n.Error != "" {
				row["error"] = n.Error
			}
			rows = append(rows, row)
		}
	}
	delegate.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "run_summary", Attrs: map[string]any{
		"status": out.Status, "duration_ms": time.Since(start).Milliseconds(), "nodes": rows,
	}})
	attrs := map[string]any{"run_id": out.RunID, "status": out.Status, "hints": map[string]string{
		"status": "sparkwing runs status --run " + out.RunID,
		"logs":   "sparkwing runs logs --run " + out.RunID,
	}}
	level := "info"
	if out.Status != "success" {
		level = "error"
	}
	if out.Err != nil {
		attrs["error"] = out.Err.Error()
	}
	delegate.Emit(sparkwing.LogRecord{TS: time.Now(), Level: level, Event: "run_finish", Attrs: attrs})
}

// safety: like capacityFingerprint, it covers the edges and concurrency memberships, which change what a run costs.
func hostedCapacityFingerprint(doc *nodeproto.PlanDoc) string {
	edges := make([]planEdge, 0, len(doc.Nodes))
	var conc []any
	for _, n := range doc.Nodes {
		edges = append(edges, planEdge{ID: n.ID, Deps: n.Deps})
		if m := n.Modifiers; m != nil && m.ConcGroup != "" {
			conc = append(conc, []any{n.ID, m.ConcGroup, m.ConcCapacity, m.ConcScope, m.ConcOnLimit, m.ConcCost})
		}
	}
	for _, pc := range doc.PlanConcs {
		conc = append(conc, []any{"", pc.Key, pc.Capacity, pc.OnLimit, pc.Cost})
	}
	return hashCanonicalJSON(map[string]any{"edges": edges, "concurrency": conc})
}

// RunHostedCommand runs one `sparkwing run` through [RunHosted] and renders it
// as the pipeline binary did, with logFormat and opts.Verbose choosing the
// renderer. The error says why the run did not succeed.
func RunHostedCommand(ctx context.Context, opts Options, entry HostedEntry, logFormat string) (*Result, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	// safety: the host's own diagnostics, such as retries and cache misses,
	// went to the pipeline binary's stderr at info; here they would print raw
	// between the renderer's records, so only warnings show unless verbose.
	if !opts.Verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	}
	if opts.MaxParallel == 0 {
		if err := applyCIEmbeddedEnv(&opts); err != nil {
			return nil, err
		}
	}
	if opts.MaxParallel == 0 {
		opts.MaxParallel = runtime.NumCPU()
	}
	delegate := runRenderer(logFormat, opts.Verbose)
	opts.Delegate = delegate
	opts.AdmissionClass = admissionClassFromEnv()
	opts.Admission = pipelineAdmission("", wingwire.OriginLocal)
	// safety: a sparkwing run started from a node's step attaches to what that node holds, which the node's
	// environment names, so it never queues behind the node waiting on it.
	if run, node := os.Getenv("SPARKWING_RUN_ID"), os.Getenv("SPARKWING_NODE_ID"); run != "" && node != "" {
		opts.Admission.ParentRunID, opts.Admission.ParentNodeID = run, node
	}
	res, err := RunHosted(ctx, paths, opts, entry)
	if renderer, ok := delegate.(*compactRunRenderer); ok {
		renderer.finishResult(res, err)
		if rerr := renderer.Err(); rerr != nil {
			return res, fmt.Errorf("run output: %w", rerr)
		}
	}
	switch {
	case err != nil:
		return res, errors.New(runDisplayError(delegate, err))
	case res.Status == "success":
		return res, nil
	case res.Error != nil:
		return res, errors.New(runDisplayError(delegate, res.Error))
	}
	return res, fmt.Errorf("run %s", res.Status)
}

type hostedChildren struct {
	state       StateBackend
	concurrency ConcurrencyBackend
	admission   *LocalAdmission
	masker      *secrets.Masker
	dryRun      bool
}

// safety: the trigger records the parent run and node, which whichever process hosts the child attaches to
// through the admission daemon, so the child never queues behind the node waiting on it.
func (h hostedChildren) StartChild(ctx context.Context, parent nodehost.ChildParent, req nodehost.ChildRequest) (string, error) {
	var env map[string]string
	// safety: a child of a dry run is a dry run, recorded on its trigger so it holds however it is dispatched.
	if h.dryRun {
		env = map[string]string{envDryRun: "1"}
	}
	id, err := enqueueTriggerWithEnv(ctx, h.state, req.Pipeline, req.Args, parent.RunID, parent.NodeID, "",
		"await-pipeline", "", req.Repo, req.Branch, env)
	if errors.Is(err, errTriggerCycle) {
		return "", errors.Join(nodehost.ErrChildCycle, err)
	}
	if err != nil {
		return "", err
	}
	if payload, err := json.Marshal(map[string]any{"child_run_id": id, "pipeline": req.Pipeline, "args": req.Args}); err == nil {
		noteEvent(ctx, h.state, parent.RunID, parent.NodeID, "child_run_start", h.masker.MaskJSON(payload))
	}
	return id, nil
}

func (h hostedChildren) Queued(ctx context.Context, runID string) (bool, error) {
	st, err := childAdmissionStatus(ctx, h.state, h.concurrency, h.admission, runID)
	return st.Status == childPlanAdmissionQueued, err
}

func (h hostedChildren) GetRun(ctx context.Context, runID string) (*store.Run, error) {
	return h.state.GetRun(ctx, runID)
}

// safety: a node's child runs queue on this machine's trigger queue, and this run drains its own children
// there as the dispatcher did; stop waits for the loop's last store write, which races the store's close.
func hostedTriggerLoop(ctx context.Context, state StateBackend, runID, repoDir string, standalone *standaloneRun) (stop func(), err error) {
	budget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}
	child := childStoreEnv{}
	if standalone != nil {
		child = childStoreEnv{path: standalone.stateDB, reason: standalone.reason}
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLocalTriggerLoop(loopCtx, state, runID, repoDir, nil, budget, child)
	}()
	return func() { cancel(); <-done }, nil
}

// safety: what a node prints to stdout is stored at info but rendered at warn, as the dispatcher's local
// runner did, so the compact renderer, which drops info, still shows it.
type hostedNodeLog struct {
	NodeLog
	delegate sparkwing.Logger
}

func (l hostedNodeLog) Emit(rec sparkwing.LogRecord) {
	l.NodeLog.Emit(rec)
	l.delegate.Emit(rec)
}

func (l hostedNodeLog) EmitText(rec sparkwing.LogRecord) {
	l.NodeLog.Emit(rec)
	if rec.Level == nodehost.LevelInfo {
		rec.Level = nodehost.LevelWarn
	}
	l.delegate.Emit(rec)
}

// safety: a retry copies the nodes its source run already finished instead of running them, as the dispatcher
// did, and an automatic retry with causes copies every node but those and what they reach.
func hostedRehydration(ctx context.Context, state StateBackend, opts Options, runID string) (*nodehost.Rehydration, error) {
	if opts.RetryOf == "" || opts.Full {
		return nil, nil
	}
	r := &nodehost.Rehydration{SourceRunID: opts.RetryOf}
	if opts.RetrySource != store.RetrySourceAuto {
		return r, nil
	}
	row, err := state.GetRun(ctx, runID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, fmt.Errorf("read retry run %s: %w", runID, err)
	default:
		r.Causes = row.RetryCauseNodeIDs
	}
	return r, nil
}

// safety: a retry copies its source run's results only when it planned the same plan; the plan names its own
// run, so the retry's is compared under the source run's id, as the source's hash was taken.
func retryPlanMatches(snapshot []byte, runID, retryOf, hash string) error {
	if hash == "" {
		return nil
	}
	source := bytes.Replace(snapshot, []byte(`"run_id":"`+runID+`"`), []byte(`"run_id":"`+retryOf+`"`), 1)
	if got := hashBytes(source); got != hash {
		return fmt.Errorf("retry provenance drift: source plan %s, checkout plan %s", hash, got)
	}
	return nil
}
