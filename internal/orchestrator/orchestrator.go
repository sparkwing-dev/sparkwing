package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Options struct {
	Pipeline string

	RunID string

	RunHandlePath string

	Args map[string]string

	Trigger sparkwing.TriggerInfo

	Git *sparkwing.Git

	Delegate sparkwing.Logger

	Runner runner.Runner

	ProcessPerNode bool

	ParentRunID string

	Admission *LocalAdmission

	// AllowUnadmitted is `sparkwing run --sw-allow-unadmitted`: run without
	// the admission daemon even when one answers.
	AllowUnadmitted bool

	// Verbose is the debug rendering --sw-verbose asks for, recorded so the
	// reproducer keeps it.
	Verbose bool

	RetryOf string

	RetrySource string

	RetryRepoDir      string
	RetryRepoIdentity string
	RetryRevision     string
	RetryPlanHash     string

	Full bool

	StartAt string
	StopAt  string

	Only string

	NoCache bool

	DryRun bool

	Debug DebugDirectives

	SecretSource secrets.Source

	PipelineYAML *pipelines.Pipeline

	MaxParallel int

	DispatchWaitTimeout time.Duration

	ArtifactStore storage.ArtifactStore

	State storage.StateStore

	DefaultStateDB string

	// Priority is the operator's --sw-priority request, unresolved: an
	// integer, or "front" / "back". Empty leaves the plan's own priority
	// alone.
	Priority string

	// AdmissionClass is an inferred trigger class. An explicit class on the
	// plan takes precedence.
	AdmissionClass sparkwing.AdmissionClass

	// safety: set only after a store is chosen, so a caller that builds its
	// own Options cannot claim a run reached the standalone store.
	standalone *standaloneRun

	// safety: set where the local store is chosen as the secret source, so a
	// preflight failure names that store's fix and never a caller's source.
	localSecrets bool

	priority    priorityRequest
	prioritySet bool

	DefaultArgs map[string]string
}

type DebugDirectives struct {
	PauseBefore []string

	PauseAfter []string

	PauseOnFailure bool
}

type Result struct {
	RunID  string
	Status string
	Error  error
}

type runInterruptedError struct {
	signal os.Signal
}

func (e *runInterruptedError) Error() string {
	return fmt.Sprintf("interrupted by %s", signalName(e.signal))
}

func signalName(sig os.Signal) string {
	switch sig {
	case os.Interrupt:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	default:
		return sig.String()
	}
}

// safety: closeAll releases what was opened in reverse order, as the defers it replaces did.
func localRunBackends(ctx context.Context, paths Paths, opts *Options) (_ Backends, closeAll func() error, err error) {
	var closers []func() error
	closeAll = func() error {
		var errs error
		for i := len(closers) - 1; i >= 0; i-- {
			errs = errors.Join(errs, closers[i]())
		}
		return errs
	}
	defer func() {
		if err != nil {
			warnHostedClose("run backends", closeAll())
		}
	}()
	if err := paths.EnsureRoot(); err != nil {
		return Backends{}, closeAll, fmt.Errorf("ensure sparkwing root: %w", err)
	}
	if opts.DefaultStateDB == "" {
		opts.DefaultStateDB = paths.StateDB()
	}
	ownsState := opts.State == nil
	hosted, selection, closeHosted, err := hostedBackendsForRun(ctx, paths, opts)
	if err != nil {
		return Backends{}, closeAll, err
	}
	closers = append(closers, func() error { closeHosted(); return nil })
	if hosted.APISocket != "" {
		// safety: profile resolution opens this machine's store only when no
		// state backend is set yet, so the daemon's client goes in before it
		// runs and the run opens nothing.
		opts.State = hosted.State
	} else if selection.standalone != "" {
		st, sa, release, serr := openStandaloneStore(paths, opts.DryRun)
		if serr != nil {
			return Backends{}, closeAll, serr
		}
		sa.reason = selection.standalone
		sa.warning = standaloneWarning(selection, sparkwingModuleVersion())
		opts.State = st
		opts.DefaultStateDB = sa.stateDB
		opts.standalone = sa
		// safety: they unwind handle first, then the decision this run's
		// ending settles, then the lock: a discard must never run while this
		// run's own handle still holds the file it is removing.
		closers = append(closers,
			func() error { release(); return nil },
			func() error { sa.settle(); return nil },
			func() error { warnHostedClose("standalone store", st.Close()); return nil })
		ownsState = false
	}
	// safety: a standalone run holds the store the fallback already chose, so
	// backend resolution must not reopen or replace it.
	keepState := hosted.APISocket != "" || opts.standalone != nil
	if err := resolveLocalBackends(opts, paths, keepState); err != nil {
		return Backends{}, closeAll, err
	}
	if hosted.APISocket != "" {
		hosted.Artifact = opts.ArtifactStore
		return hosted, closeAll, nil
	}
	st, ok := opts.State.(*store.Store)
	if !ok {
		return Backends{}, closeAll, fmt.Errorf("state backend: unrecognized implementation %T", opts.State)
	}
	if ownsState {
		closers = append(closers, st.Close)
	}
	return LocalBackends(paths, st, opts.ArtifactStore), closeAll, nil
}

func mergeInvokeArgs(opts Options) map[string]string {
	var pipelineArgs map[string]string
	if opts.PipelineYAML != nil {
		pipelineArgs = opts.PipelineYAML.Args
	}
	if len(opts.DefaultArgs) == 0 && len(pipelineArgs) == 0 {
		return opts.Args
	}
	merged := make(map[string]string, len(opts.DefaultArgs)+len(pipelineArgs)+len(opts.Args))
	maps.Copy(merged, opts.DefaultArgs)
	maps.Copy(merged, pipelineArgs)
	maps.Copy(merged, opts.Args)
	return merged
}

// safety: a parse error describes the author's own config, so a failed run
// records it in full rather than the generic setup-failure text.
type projectConfigError struct{ err error }

func (e *projectConfigError) Error() string { return "project config: " + e.err.Error() }

func (e *projectConfigError) Unwrap() error { return e.err }

func buildRunInvocation(opts Options, runID, logDir string, secretArgs []string) map[string]any {
	inv := map[string]any{
		"run_id":   runID,
		"pipeline": opts.Pipeline,
		"hints": map[string]string{
			"follow_logs": "sparkwing runs logs --run " + runID + " --follow",
			"status":      "sparkwing runs status --run " + runID,
		},
	}
	if logDir != "" {
		inv["log_path"] = logDir
	}
	if wd := sparkwing.WorkDir(); wd != "" {
		inv["cwd"] = wd
	} else if cwd, err := os.Getwd(); err == nil && cwd != "" {
		inv["cwd"] = cwd
	}
	if len(opts.Args) > 0 {
		args := make(map[string]string, len(opts.Args))
		for k, v := range opts.Args {
			args[k] = v
		}
		inv["args"] = args
		if !containsNamedArg(opts.Args, secretArgs) {
			inv["inputs_hash"] = hashCanonicalJSON(opts.Args)
		}
	}

	if len(secretArgs) > 0 {
		inv[store.InvocationSecretArgsKey] = secretArgs
	}
	if opts.standalone != nil && opts.standalone.reason != "" {
		inv["standalone"] = true
		inv["standalone_reason"] = opts.standalone.reason
	}
	if opts.RetryRepoDir != "" || opts.RetryRepoIdentity != "" || opts.RetryRevision != "" || opts.RetryPlanHash != "" {
		inv["retry_provenance"] = map[string]string{
			"repo_dir":       opts.RetryRepoDir,
			"repo_identity":  opts.RetryRepoIdentity,
			"revision":       opts.RetryRevision,
			"plan_hash":      opts.RetryPlanHash,
			"content_policy": retryprovenance.RecordedRevisionSnapshotPolicy,
		}
	}
	if flags := buildRunFlags(opts); len(flags) > 0 {
		inv["flags"] = flags
	}
	inv["reproducer"] = buildReproducer(opts, runID)
	return inv
}

func containsNamedArg(args map[string]string, names []string) bool {
	for _, name := range names {
		if _, ok := args[name]; ok {
			return true
		}
	}
	return false
}

func emitRunStart(delegate sparkwing.Logger, invocation map[string]any, diskRoot string) {
	if delegate == nil {
		return
	}
	delegate.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "run_start",
		Attrs: withDiskAttrs(store.RedactInvocation(invocation), diskRoot),
	})
}

func buildRunFlags(opts Options) map[string]any {
	flags := map[string]any{}
	if opts.RetryOf != "" {
		flags["retry_of"] = opts.RetryOf
	}
	if opts.Full {
		flags["full"] = true
	}
	runflags.Flags{StartAt: opts.StartAt, StopAt: opts.StopAt, Only: opts.Only, DryRun: opts.DryRun, NoCache: opts.NoCache}.Record(flags)
	if opts.MaxParallel > 0 {
		flags["max_parallel"] = opts.MaxParallel
	}
	if opts.Verbose {
		flags["verbose"] = true
	}
	if opts.prioritySet {
		flags["priority"] = opts.priority.value
		flags["priority_source"] = opts.priority.source
	}
	return flags
}

func hashCanonicalJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashBytes(buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	sum := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func buildReproducer(opts Options, _ string) string {
	return reproducerFor(opts.Pipeline, buildRunFlags(opts), opts.Args)
}

// safety: every recorded flag is rendered with the --sw- spelling sparkwing run accepts today, so the line
// re-runs what it records; retry_of and full come from `sparkwing runs retry`, not run flags, and are left out.
func reproducerFor(pipeline string, flags map[string]any, args map[string]string) string {
	parts := []string{"sparkwing", "run", pipeline}
	flagKeys := make([]string, 0, len(flags))
	for k := range flags {
		flagKeys = append(flagKeys, k)
	}
	sort.Strings(flagKeys)
	for _, k := range flagKeys {
		if k == "max_parallel" || k == "priority_source" || k == "retry_of" || k == "full" {
			continue
		}
		flagName := "--sw-" + strings.ReplaceAll(k, "_", "-")
		switch v := flags[k].(type) {
		case bool:
			if v {
				parts = append(parts, flagName)
			}
		case string:
			if v != "" {
				parts = append(parts, flagName+"="+v)
			}
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", flagName, v))
		}
	}
	argKeys := make([]string, 0, len(args))
	for k := range args {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	for _, k := range argKeys {
		parts = append(parts, "--"+k+"="+args[k])
	}
	return strings.Join(parts, " ")
}

type planEdge struct {
	ID   string   `json:"id"`
	Deps []string `json:"deps"`
}

var defaultPauseTimeout = 30 * time.Minute

func canceledByRun(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	// safety: a ctx-cancelled child is SIGKILLed; its ExitError carries
	// no wrapped context error, only the "signal: killed" text.
	return strings.Contains(err.Error(), "signal: killed")
}

func snapshotPipelineRefs(refs []sparkwing.RefTarget) []nodeproto.PlanPipelineRef {
	var out []nodeproto.PlanPipelineRef
	for _, r := range refs {
		out = append(out, nodeproto.PlanPipelineRef{Pipeline: r.Pipeline, Node: r.NodeID})
	}
	return out
}

type planSnapshotMeta struct {
	Secrets          pipelines.SecretsField
	PipelineRequires []string
}

func buildPlanSnapshot(p *sparkwing.Plan, rc sparkwing.RunContext, meta planSnapshotMeta) (nodeproto.PlanDocOld, error) {
	snap := nodeproto.PlanDocOld{
		Protocol:       authwire.NodeProtocolVersion,
		AdmissionClass: string(p.AdmissionClassValue()),
		Pipeline:       rc.Pipeline,
		RunID:          rc.RunID,
		Priority:       p.PriorityValue(),
		ClaimWaitMS:    p.ClaimWaitValue().Milliseconds(),
		Requires:       slices.Clone(meta.PipelineRequires),
		Secrets:        meta.Secrets,
		// safety: an empty plan still writes "nodes": [], the shape every SDK and reader expects.
		Nodes: []nodeproto.PlanNode{},
	}
	for _, membership := range p.PlanConcurrency() {
		if membership.Group == nil {
			continue
		}
		limit := membership.Group.Limit()
		pc := nodeproto.PlanConc{
			Key:             scopedGroupKey(membership.Group, rc.RunID),
			Capacity:        limit.Capacity,
			Cost:            membership.Cost,
			OnLimit:         string(limit.OnLimit),
			QueueTimeoutMS:  limit.QueueTimeout.Milliseconds(),
			CancelTimeoutMS: limit.CancelTimeout.Milliseconds(),
		}
		if snap.PlanConc == nil {
			snap.PlanConc = &pc
		}
		snap.PlanConcs = append(snap.PlanConcs, pc)
	}
	if rh := p.ResourceHints(); rh != nil {
		snap.Resources = &nodeproto.PlanResources{
			Cores:       rh.Cores,
			MemoryBytes: rh.MemoryBytes,
		}
	}
	seen := make(map[string]bool)
	for _, n := range p.Nodes() {
		sn := nodeproto.PlanNode{
			ID:           n.ID(),
			Deps:         n.DepIDs(),
			OptionalDeps: n.OptionalDepIDs(),
			Env:          n.EnvMap(),
			Outputs:      n.OutputGlobs(),
			Consumes:     snapshotConsumeEdges(n.ConsumeEdges()),
			Groups:       p.JobGroupNames(n.ID()),
			Dynamic:      p.IsDynamicNode(n.ID()),
			PipelineRefs: snapshotPipelineRefs(n.PipelineRefs()),
		}
		if cfg := n.ApprovalConfigOld(); cfg != nil {
			sn.Approval = &nodeproto.PlanApproval{
				Message:   cfg.Message,
				TimeoutMS: cfg.Timeout.Milliseconds(),
				OnTimeout: string(cfg.OnExpiry),
			}
		}
		snapshotNodeEnvelope(p, n, &sn)
		snap.Nodes = append(snap.Nodes, sn)
		seen[n.ID()] = true
	}
	for _, n := range p.Nodes() {
		rec := n.OnFailureNode()
		if rec == nil || seen[rec.ID()] {
			continue
		}
		recSnap := nodeproto.PlanNode{
			ID:           rec.ID(),
			Deps:         rec.DepIDs(),
			OptionalDeps: rec.OptionalDepIDs(),
			Env:          rec.EnvMap(),
			Outputs:      rec.OutputGlobs(),
			Consumes:     snapshotConsumeEdges(rec.ConsumeEdges()),
			Groups:       p.JobGroupNames(rec.ID()),
			OnFailureOf:  n.ID(),
			PipelineRefs: snapshotPipelineRefs(rec.PipelineRefs()),
		}
		snapshotNodeEnvelope(p, rec, &recSnap)
		snap.Nodes = append(snap.Nodes, recSnap)
		seen[rec.ID()] = true
	}
	return snap, nil
}

func snapshotNodeEnvelope(p *sparkwing.Plan, n *sparkwing.JobNode, sn *nodeproto.PlanNode) {
	sn.Modifiers = nodeModifiersSnapshot(n)
	sn.Caches = nodeDirCaches(n)
	closures, stepClosures := sparkwingruntime.NodeClosureIDs(p, n)
	sn.Closures = closures
	sn.NeedsGenerated = sparkwingruntime.NeedsGeneratedIDs(p, n)
	if closures != nil && closures.CacheKey != "" {
		sn.MemoKey = &nodeproto.PlanMemoKey{Closure: closures.CacheKey}
	}
	if w := n.Work(); w != nil {
		sn.Work = snapshotWorkOf(w, n.ResultStep(), stepClosures)
	}
}

func snapshotConsumeEdges(edges []sparkwing.ConsumeEdge) []nodeproto.PlanConsume {
	out := make([]nodeproto.PlanConsume, len(edges))
	for i, edge := range edges {
		out[i] = nodeproto.PlanConsume{Producer: edge.Producer, Into: edge.Into}
	}
	return out
}

func nodeModifiersSnapshot(n *sparkwing.JobNode) *nodeproto.PlanModifiers {
	rc := n.RetryConfig()
	m := nodeproto.PlanModifiers{
		Retry:               rc.Attempts,
		RetryBackoffMS:      rc.Backoff.Milliseconds(),
		RetryAuto:           rc.Auto,
		TimeoutMS:           n.TimeoutDuration().Milliseconds(),
		NoProgressTimeoutMS: n.NoProgressTimeoutDuration().Milliseconds(),
		RunsOn:              n.RequiresLabels(),
		Prefers:             n.PrefersLabels(),
		WhenRunner:          n.WhenRunnerLabels(),
		Inline:              n.IsInline(),
		Optional:            n.IsOptional(),
		ContinueOnError:     n.IsContinueOnError(),
		HasBeforeRun:        len(n.BeforeRunHooks()) > 0,
		HasAfterRun:         len(n.AfterRunHooks()) > 0,
		HasSkipIf:           len(n.SkipPredicates()) > 0,
		HasVerify:           n.Verifier() != nil,
		SkipIfBudgetMS:      n.SkipIfBudget().Milliseconds(),
	}
	if rec := n.OnFailureNode(); rec != nil {
		m.OnFailure = rec.ID()
	}
	if cc := n.MemoizeConfig(); cc != nil {
		m.Cache = true
		m.CacheTTLMS = cc.TTL.Milliseconds()
	}
	if g := n.ConcurrencyGroupRef(); g != nil {
		limit := g.Limit()
		m.ConcGroup = g.Name()
		m.ConcCapacity = limit.Capacity
		m.ConcCost = n.ConcurrencyCost()
		m.ConcScope = string(limit.Scope)
		m.ConcOnLimit = string(limit.OnLimit)
		m.ConcQueueTimeoutMS = limit.QueueTimeout.Milliseconds()
		m.ConcCancelTimeoutMS = limit.CancelTimeout.Milliseconds()
	}
	if rh := n.ResourceHints(); rh != nil {
		m.ResCores = rh.Cores
		m.ResMemoryBytes = rh.MemoryBytes
	}
	if isZeroModifiers(m) {
		return nil
	}
	return &m
}

func isZeroModifiers(m nodeproto.PlanModifiers) bool {
	return m.Retry == 0 &&
		m.RetryBackoffMS == 0 &&
		!m.RetryAuto &&
		m.TimeoutMS == 0 &&
		m.NoProgressTimeoutMS == 0 &&
		len(m.RunsOn) == 0 &&
		len(m.Prefers) == 0 &&
		len(m.WhenRunner) == 0 &&
		!m.Cache &&
		m.CacheTTLMS == 0 &&
		m.ConcGroup == "" &&
		m.ConcCapacity == 0 &&
		m.ConcCost == 0 &&
		m.ConcScope == "" &&
		m.ConcOnLimit == "" &&
		m.ConcQueueTimeoutMS == 0 &&
		m.ConcCancelTimeoutMS == 0 &&
		m.ResCores == 0 &&
		m.ResMemoryBytes == 0 &&
		!m.Inline &&
		!m.Optional &&
		!m.ContinueOnError &&
		m.OnFailure == "" &&
		!m.HasBeforeRun &&
		!m.HasAfterRun &&
		!m.HasSkipIf &&
		!m.HasVerify &&
		m.SkipIfBudgetMS == 0
}

func snapshotWorkOf(work *sparkwing.Work, resultStep *sparkwing.WorkStep, closures map[string]*nodeproto.PlanClosures) *nodeproto.PlanWork {
	out := &nodeproto.PlanWork{FailurePolicy: string(work.ParallelFailurePolicy())}
	if resultStep != nil {
		out.ResultStep = resultStep.ID()
	}
	for _, s := range work.Steps() {
		out.Steps = append(out.Steps, nodeproto.PlanStep{
			ID:        s.ID(),
			Needs:     s.DepIDs(),
			IsResult:  s == resultStep,
			HasSkipIf: len(s.SkipPredicates()) > 0,
			Finally:   s.IsFinally(),
			Risks:     s.Risks(),

			ContinueOnError:   s.IsContinueOnError(),
			Optional:          s.IsOptional(),
			HasDryRun:         s.HasDryRun(),
			SafeWithoutDryRun: s.IsSafeWithoutDryRun(),
			Closures:          closures[s.ID()],
		})
	}
	for _, g := range work.Groups() {
		members := g.Members()
		ids := make([]string, len(members))
		for i, m := range members {
			ids[i] = m.ID()
		}
		out.StepGroups = append(out.StepGroups, nodeproto.PlanStepGroup{
			Name:    g.Name(),
			Members: ids,
		})
	}
	return out
}

func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [16]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}
