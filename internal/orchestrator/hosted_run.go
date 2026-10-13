package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
	// Program is run as "<Program> --sw-node-protocol" in Dir, the repository
	// root, with Env, the user's environment.
	Program string
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
// node runs in its own SDK process of entry.Program, and this process owns
// the DAG, the store writes and the envelope.
func RunHosted(ctx context.Context, paths Paths, opts Options, entry HostedEntry) (res *Result, err error) {
	if err := applyHostedProjectConfig(&opts, entry.Dir); err != nil {
		return nil, err
	}
	backends, closeBackends, err := localRunBackends(ctx, paths, &opts)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeBackends()) }()
	invokeArgs := mergeInvokeArgs(opts)
	if opts.PipelineYAML != nil {
		guards := pipelines.GuardContext{Args: invokeArgs, ProfileIsLocal: backends.LocalCoordination}
		if opts.Git != nil {
			guards.GitBranch, guards.GitDefaultBranch = opts.Git.Branch, opts.Git.DefaultBranch
		}
		if err := opts.PipelineYAML.Guards.Evaluate(opts.Pipeline, guards); err != nil {
			return nil, err
		}
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

	masker := secrets.NewMasker()
	for _, name := range entry.SecretArgs {
		if v := invokeArgs[name]; v != "" {
			masker.Register(v)
		}
	}
	delegate := secrets.MaskingLogger(opts.Delegate, masker)
	routes, err := nodehost.Start(slog.Default())
	if err != nil {
		return nil, err
	}
	//nolint:contextcheck // Close bounds its own shutdown; the run's context may already be cancelled here.
	defer func() { warnHostedClose("node routes", routes.Close()) }()
	state := &hostedRunState{StateBackend: backends.State, logs: backends.Logs, delegate: delegate, diskRoot: backends.DiskRoot, starts: map[string]time.Time{}}
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
	}
	if opts.RunHandlePath != "" {
		release, err := ReserveRunHandle(opts.RunHandlePath)
		if err != nil {
			return nil, fmt.Errorf("reserve run handle: %w", err)
		}
		defer release()
		if err := PublishRunHandle(opts.RunHandlePath, NewRunHandle(runID, opts.Pipeline, logDir, "running")); err != nil {
			return nil, fmt.Errorf("publish run handle: %w", err)
		}
	}
	emitRunStart(opts.Delegate, invocation, backends.DiskRoot)
	opts.standalone.announce()

	resolver := secrets.NewCached(localSecretsFor(ctx, paths, backends, runID, opts.Pipeline), masker)
	opts.localSecrets = true
	if p := opts.PipelineYAML; p != nil {
		state.requires = p.Requires
	}
	state.preflight = func(ctx context.Context, doc nodeproto.PlanDoc) error {
		return hostedSecretPreflight(ctx, resolver, doc, opts)
	}
	attempts := &nodehost.Attempts{
		Routes: routes, Masker: masker, Resolver: resolver,
		Local: backends.State,
		OpenLog: func(ctx context.Context, runID, nodeID string) (nodehost.NodeLog, error) {
			return backends.Logs.OpenNodeLog(ctx, runID, nodeID, delegate)
		},
	}
	if backends.LocalCoordination {
		attempts.Usage = nodehost.RecordUsage(backends.State)
	}
	cfg := nodehost.Config{
		Session:  nodehost.SessionConfig{Entry: entry.Program, Dir: entry.Dir, Env: hostedEnv(entry.Env), Sink: hostedPlanSink{delegate}},
		Pipeline: opts.Pipeline, Args: invokeArgs, Run: hostedRunContext(opts, runID),
		StartAt: opts.StartAt, StopAt: opts.StopAt,
		State: state, Artifacts: backends.Artifact, Attempts: attempts, Admission: admission,
		Debug: nodehost.Debug{
			PauseBefore: opts.Debug.PauseBefore, PauseAfter: opts.Debug.PauseAfter,
			PauseOnFailure: opts.Debug.PauseOnFailure, PauseTimeout: pauseTimeout(), Bounces: hostedBounces(backends.State),
		},
		Invocation: invocation,
		Memo: &nodehost.Memo{
			Slots: backends.Concurrency, NoCache: opts.NoCache,
			Output: func(ctx context.Context, _ store.ClaimInputRequest, runID, nodeID string) ([]byte, error) {
				return backends.State.GetNodeOutput(ctx, runID, nodeID)
			},
		},
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
		if len(p.Requires) == 0 {
			p.Requires = cfg.Defaults.Requires
		}
	}
	return RefusePinnedLocalRun(opts.PipelineYAML)
}

// safety: the engine's own SPARKWING_* variables never reach pipeline code,
// in the plan process as in every attempt.
func hostedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "SPARKWING_") {
			out = append(out, kv)
		}
	}
	return out
}

func hostedRunContext(opts Options, runID string) nodeproto.RunContext {
	rc := nodeproto.RunContext{
		RunID: runID, NoCache: opts.NoCache, DryRun: opts.DryRun,
		Trigger: &nodeproto.Trigger{Source: opts.Trigger.Source, User: opts.Trigger.User},
	}
	if rc.Trigger.Source == "" {
		rc.Trigger.Source = "manual"
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

// safety: plan code's logs have no node log to land in, so they go to the renderer.
type hostedPlanSink struct{ delegate sparkwing.Logger }

func (s hostedPlanSink) Text(level, line string) {
	s.delegate.Emit(sparkwing.LogRecord{TS: time.Now(), Level: level, Msg: line})
}

func (s hostedPlanSink) Record(rec nodeproto.LogRecord) {
	s.delegate.Emit(sparkwing.LogRecord{TS: time.Now(), Level: rec.Level, Event: rec.Event, Msg: rec.Msg})
}

// hack: the host has no event seam yet, so the run_plan, node_start and node_end records the dispatcher wrote
// come from the state writes that mark those moments.
type hostedRunState struct {
	StateBackend
	logs     LogBackend
	delegate sparkwing.Logger
	diskRoot string

	preflight func(context.Context, nodeproto.PlanDoc) error
	requires  []string

	mu     sync.Mutex
	doc    *nodeproto.PlanDoc
	starts map[string]time.Time
}

func (s *hostedRunState) UpdatePlanSnapshot(ctx context.Context, runID string, snapshot []byte) error {
	snapshot, err := withHostedRequires(snapshot, s.requires)
	if err != nil {
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

// safety: requires is the host's placement constraint from sparkwing.yaml, which the SDK's plan never carries,
// so the accepted snapshot gets it here as the dispatcher's snapshot did.
func withHostedRequires(snapshot []byte, requires []string) ([]byte, error) {
	if len(requires) == 0 {
		return snapshot, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return nil, fmt.Errorf("plan snapshot: %w", err)
	}
	raw, err := json.Marshal(requires)
	if err != nil {
		return nil, err
	}
	doc["requires"] = raw
	return json.Marshal(doc)
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
// as the pipeline binary did, with logFormat and verbose choosing the
// renderer. The error says why the run did not succeed.
func RunHostedCommand(ctx context.Context, opts Options, entry HostedEntry, logFormat string, verbose bool) (*Result, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	// safety: the host's own diagnostics, such as retries and cache misses,
	// went to the pipeline binary's stderr at info; here they would print raw
	// between the renderer's records, so only warnings show unless verbose.
	if !verbose {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	}
	delegate := runRenderer(logFormat, verbose)
	opts.Delegate = delegate
	opts.AdmissionClass = admissionClassFromEnv()
	opts.Admission = pipelineAdmission(childAttachTokenFromProcessEnv(), wingwire.OriginLocal)
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
