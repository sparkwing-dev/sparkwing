package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Options configure a run. The same Options works for local or
// cluster mode.
type Options struct {
	// Pipeline is the registered pipeline name.
	Pipeline string

	// RunID, when non-empty, overrides ID generation. Controllers
	// pre-assign IDs at trigger time.
	RunID string

	// Args are untyped invocation arguments.
	Args map[string]string

	// Trigger describes how the run was initiated. Defaults to
	// {Source: "manual"} when zero.
	Trigger sparkwing.TriggerInfo

	// Git is the run's view of the working tree. Plumbed onto
	// RunContext.Git and Runtime().Git. Nil is acceptable for
	// untracked runs.
	Git *sparkwing.Git

	// Delegate, when set, mirrors every log line for interactive
	// display.
	Delegate sparkwing.Logger

	// Runner, when non-nil, replaces the default InProcessRunner.
	Runner runner.Runner

	// ParentRunID marks this run as spawned by another via
	// RunAndAwait; the controller walks the ancestor chain for
	// cross-pipeline cycle detection.
	ParentRunID string

	// Admission, when non-nil, routes the run through the local
	// admission daemon: one all-or-nothing lease acquired after the plan
	// is built, held by an open connection for the run's lifetime, and
	// released after the run row is finalized.
	//
	// Admission belongs to whoever owns the machine. The local entry
	// points (a pipeline binary run and handle-trigger --local) set this
	// because sparkwingd owns the laptop. Cluster paths -- the worker,
	// run-node, and every pod-executed mode -- leave it nil deliberately:
	// the Kubernetes scheduler admitted the pod before the process
	// started, and a second admission layer would only fight it. With a
	// nil Admission no daemon is contacted, host resources are not
	// gated, and every concurrency scope keeps its shared-store path.
	Admission *LocalAdmission

	// RetryOf is the run id this execution retries. Drives skip-passed
	// rehydration unless Full is set.
	RetryOf string

	// RetrySource is store.RetrySourceManual or RetrySourceAuto.
	RetrySource string

	// Full disables skip-passed rehydration on retry.
	Full bool

	// StartAt / StopAt name a WorkStep (or Spawn) where the run
	// resumes / stops, inclusive on both bounds. Items outside the
	// resulting reachability window are skipped with a `step_skipped`
	// event whose reason carries "outside --start-at..--stop-at
	// range". Empty values disable the bound. The orchestrator
	// validates the strings against every Work's registered ids
	// before dispatching; unknown ids fail the run with a
	// Levenshtein-suggesting "did you mean X?" message.
	StartAt string
	StopAt  string

	// Only filters the plan's JobNodes to those whose IDs match the
	// path.Match glob, transitively pulling Needs() ancestors so the
	// resulting dispatch is self-consistent. Unmatched (and
	// non-ancestor) jobs are skipped with `node_skipped` and reason
	// `outside --only=<pattern>`. Empty disables the filter. Rejected
	// in combination with StartAt/StopAt: they are a different filter
	// mode (step-level reachability) and intersecting them produces
	// surprising selections. Operates on the statically-known plan;
	// dynamically expanded group members run when their parent is in
	// the keep-set.
	Only string

	// NoCache disables cache READS on this run's per-node Cache()
	// lookups. Cache WRITES still occur on success, so subsequent runs
	// over the same content hit cache normally. Distinct from the
	// SPARKWING_NO_BINCACHE flag that gates the compiled-pipeline-binary
	// (bincache) cache.
	NoCache bool

	// LocalOnly forces SQLite state, filesystem cache, and filesystem
	// logs for this run regardless of backends.yaml, env-var shims, or
	// any other shared-backend config. Used as the escape hatch from
	// the shared-backends story: an operator hitting a stale or
	// unreachable shared store can bypass the resolver entirely and
	// run as if no shared infrastructure were configured.
	LocalOnly bool

	// DryRun selects the no-mutation dispatch path: every step's
	// DryRunFn (or its apply Fn when the step is explicitly marked
	// SafeWithoutDryRun) runs in place of the apply Fn. Steps that
	// declared neither are soft-skipped with reason
	// `no_dry_run_defined` so existing pipelines keep working under
	// `--dry-run` while making the contract gap visible in the run
	// logs.
	DryRun bool

	// Debug carries pause directives populated by `sparkwing debug run`.
	Debug DebugDirectives

	// SecretSource is wrapped in a per-run cache and installed as the
	// SecretResolver. Nil means Secret() errors.
	SecretSource secrets.Source

	// PipelineYAML is the on-disk pipelines.yaml entry for this run's
	// pipeline. When non-nil, the orchestrator resolves Config and
	// Secrets via the SDK helpers (sparkwing.ResolvePipelineConfig /
	// ResolvePipelineSecrets) before invoking the registration, so
	// step bodies can read the typed values through
	// sparkwing.PipelineConfig[T](ctx) / PipelineSecrets[T](ctx).
	// Nil leaves both surfaces empty (existing pipelines unaffected).
	PipelineYAML *pipelines.Pipeline

	// SparkwingDir, when non-empty, is the resolved .sparkwing/
	// directory. Used today for working-directory context; secret
	// source binding now reads the inline spec on
	// PipelineYAML.Dispatch.Source rather than a registry file.
	SparkwingDir string

	// MaxParallel caps concurrent node execution. Zero = unbounded
	// (cluster default); local mode sets NumCPU. The cap applies only
	// to active execution; dep-wait goroutines are uncapped.
	MaxParallel int

	// DispatchWaitTimeout bounds the dispatcher's post-DAG drain. If
	// every per-node goroutine hasn't returned within the duration,
	// the dispatcher emits a `dispatch_wait_timeout` event with a
	// goroutine stack dump and returns -- which fires the deferred
	// concurrency-slot release so a wedged run doesn't lock the rest
	// of the fleet behind a process that will never complete. Zero
	// uses DefaultDispatchWaitTimeout (30m). Negative disables the
	// watchdog entirely (historical wait-forever behavior).
	DispatchWaitTimeout time.Duration

	// LogStore, when non-nil, replaces the default filesystem
	// LogBackend used by RunLocal.
	LogStore storage.LogStore

	// ArtifactStore receives the final run-state NDJSON dump
	// (runs/<runID>/state.ndjson) after RunLocal exits.
	ArtifactStore storage.ArtifactStore

	// State, when non-nil, is the run-record store RunLocal wraps into
	// LocalBackends. ApplyProfileBackends populates this from the resolved
	// profile's surfaces (with the per-target backend overlay layered on
	// top) or synthesizes a sqlite spec at paths.StateDB() when the
	// profile declares no state. Pre-set values from the caller are
	// preserved.
	State storage.StateStore

	// DefaultStateDB names the SQLite file the profile resolver falls
	// back to when the resolved profile declares no state surface and
	// the caller didn't pre-set State. RunLocal sets this to
	// paths.StateDB() so every code path opens the state store through
	// the factory. Cluster boot paths leave it empty; they wire State
	// through their own plumbing.
	DefaultStateDB string

	// ProfileLookup resolves a profile name to (controller URL, token)
	// for type=controller cache/logs/state backend specs. RunLocal
	// installs the active profile's controller as the default when
	// this is nil; tests inject a synthetic lookup pointing at an
	// httptest server. Cluster boot paths leave it nil and declare
	// URL + token via CLI flags rather than profile names.
	ProfileLookup storeurl.ProfileLookup

	// Profile is the resolved storage profile RunLocal routes
	// state/logs/cache through via ApplyProfileBackendsWithMirror. The
	// laptop path always sets it (profileFromEnv resolves the chain down
	// to the built-in laptop fallback); a nil Profile falls back to local
	// SQLite. Cluster boot paths leave it nil and wire State directly.
	Profile *profile.Profile

	// ProfileChain is the resolution chain that picked Profile (which
	// rule matched, and the alternatives considered). Set alongside
	// Profile so run_start can record *why* this profile was chosen.
	// Nil when Profile is nil; the run_start profile block is omitted
	// unless both are set.
	ProfileChain *profile.Chain

	// DefaultArgs is the project's defaults.args block from
	// sparkwing.yaml. Layers below PipelineYAML.Args (which layers
	// below the explicit CLI flag). Nil when no project defaults
	// apply.
	DefaultArgs map[string]string

	// MirrorLocal, when non-nil, is an opened local SQLite store that
	// RunLocal tees state writes to alongside the canonical state
	// backend (see mirrorStateBackend). ApplyProfileBackendsWithMirror
	// opens it for `sparkwing run --profile <non-local>` from a laptop;
	// RunLocal wraps Backends.State around it and owns closing it.
	// Cluster boot paths leave it nil so pods never carry a mirror.
	MirrorLocal *store.Store
}

// DebugDirectives is the ephemeral pause surface for one run.
type DebugDirectives struct {
	// PauseBefore holds these node IDs before dispatch.
	PauseBefore []string
	// PauseAfter holds these node IDs after the job's Run returns.
	PauseAfter []string
	// PauseOnFailure holds nodes with a non-nil Run error (excludes
	// Skipped/Cancelled/OnFailure-recovered).
	PauseOnFailure bool
}

func (d DebugDirectives) pauseBefore(id string) bool { return containsID(d.PauseBefore, id) }
func (d DebugDirectives) pauseAfter(id string) bool  { return containsID(d.PauseAfter, id) }

func containsID(list []string, id string) bool {
	for _, s := range list {
		if s == id {
			return true
		}
	}
	return false
}

// Result summarizes a finished run.
type Result struct {
	RunID  string
	Status string // "success" or "failed"
	Error  error  // non-nil when at least one node failed
}

// Run executes the pipeline to terminal state through Backends.
// Caller owns Backends lifecycle. See RunLocal for a managed wrapper.
func Run(ctx context.Context, backends Backends, opts Options) (*Result, error) {
	reg, ok := sparkwing.Lookup(opts.Pipeline)
	if !ok {
		return nil, fmt.Errorf("pipeline %q is not registered", opts.Pipeline)
	}
	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}

	trigger := opts.Trigger
	if trigger.Source == "" {
		trigger.Source = "manual"
	}

	if opts.PipelineYAML != nil {
		guardCtx := pipelines.GuardContext{
			Args: opts.Args,
		}
		if opts.Profile != nil {
			guardCtx.ProfileName = opts.Profile.Name
			guardCtx.ProfileIsLocal = opts.Profile.ControllerURL() == ""
		}
		if opts.Git != nil {
			guardCtx.GitBranch = opts.Git.Branch
			guardCtx.GitDefaultBranch = opts.Git.DefaultBranch
		}
		if err := opts.PipelineYAML.Guards.Evaluate(opts.Pipeline, guardCtx); err != nil {
			return nil, err
		}
	}

	runID := opts.RunID
	if runID == "" {
		runID = newRunID()
	}

	// safety: opts.Git may be nil for untracked dispatches; non-nil gitOpt lets rc.Git.IsDirty error instead of panic.
	gitOpt := opts.Git
	if gitOpt == nil {
		gitOpt = &sparkwing.Git{}
	}

	rc := sparkwing.RunContext{
		RunID:     runID,
		Pipeline:  opts.Pipeline,
		Git:       gitOpt,
		Trigger:   trigger,
		StartedAt: time.Now(),
	}
	sparkwing.SetGit(gitOpt)

	owner, repo := sparkwing.GithubOwnerRepo(gitOpt.Repo)
	invocation := buildRunInvocation(opts, runID)
	if err := backends.State.CreateRun(ctx, store.Run{
		ID:            runID,
		Pipeline:      opts.Pipeline,
		Status:        "running",
		ParentRunID:   opts.ParentRunID,
		RetryOf:       opts.RetryOf,
		RetrySource:   opts.RetrySource,
		TriggerSource: trigger.Source,
		GitBranch:     gitOpt.Branch,
		GitSHA:        gitOpt.SHA,
		Repo:          gitOpt.Repo,
		RepoURL:       gitOpt.RepoURL,
		GithubOwner:   owner,
		GithubRepo:    repo,
		Args:          opts.Args,
		StartedAt:     rc.StartedAt,
		Invocation:    invocation,
	}); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	hbCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go runRunHeartbeatLoop(hbCtx, 30*time.Second, backends.State, runID, wedgeBudget)

	masker := secrets.NewMasker()
	for _, v := range reg.SecretValues(opts.Args) {
		masker.Register(v)
	}

	invokeArgs := opts.Args
	pipelineArgs := map[string]string(nil)
	if opts.PipelineYAML != nil {
		pipelineArgs = opts.PipelineYAML.Args
	}
	if len(opts.DefaultArgs) > 0 || len(pipelineArgs) > 0 {
		merged := make(map[string]string, len(opts.DefaultArgs)+len(pipelineArgs)+len(invokeArgs))
		for k, v := range opts.DefaultArgs {
			merged[k] = v
		}
		for k, v := range pipelineArgs {
			merged[k] = v
		}
		for k, v := range invokeArgs {
			merged[k] = v
		}
		invokeArgs = merged
	}

	var profName string
	var profIsLocal bool
	if opts.Profile != nil {
		profName = opts.Profile.Name
		profIsLocal = opts.Profile.ControllerURL() == ""
	}
	ctx = sparkwingruntime.WithProfileResolution(ctx, sparkwing.ProfileResolutionContext{
		Name:    profName,
		IsLocal: profIsLocal,
	})

	plan, err := reg.Invoke(ctx, invokeArgs, rc)
	if err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("plan: %v", err))
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}

	snapMeta := planSnapshotMeta{
		Secrets: sparkwingruntime.ReflectSecretsField(reg),
	}
	if opts.PipelineYAML != nil {
		snapMeta.PipelineRequires = opts.PipelineYAML.Requires
	}

	snapshot, err := marshalPlanSnapshot(plan, rc, snapMeta)
	if err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("plan snapshot: %v", err))
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}
	if err := backends.State.UpdatePlanSnapshot(ctx, runID, snapshot); err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("persist snapshot: %v", err))
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}
	for _, n := range plan.Nodes() {
		if err := backends.State.CreateNode(ctx, store.Node{
			RunID:       runID,
			NodeID:      n.ID(),
			Status:      "pending",
			Deps:        n.DepIDs(),
			NeedsLabels: effectiveClaimLabels(n, snapMeta.PipelineRequires),
		}); err != nil {
			_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("create node %s: %v", n.ID(), err))
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
	}

	validatePlanModifiers(opts.Delegate, plan)

	if opts.StartAt != "" || opts.StopAt != "" {
		if opts.Only != "" {
			err := fmt.Errorf("--only is mutually exclusive with --start-at / --stop-at")
			_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
		if err := sparkwingruntime.ValidateStepRange(plan, opts.StartAt, opts.StopAt); err != nil {
			_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
		ctx = sparkwingruntime.WithStepRange(ctx, opts.StartAt, opts.StopAt)
	}
	var onlySkip map[string]string
	if opts.Only != "" {
		skip, err := computeOnlySkip(plan, opts.Only)
		if err != nil {
			_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
		onlySkip = skip
	}
	if opts.NoCache {
		ctx = withNoCache(ctx)
	}
	if opts.DryRun {
		ctx = sparkwingruntime.WithDryRun(ctx)
	}

	emitRunStart(opts.Delegate, invocation)
	emitRunPlan(opts.Delegate, plan)

	r := opts.Runner
	if r == nil {
		r = NewInProcessRunner(backends)
	}
	ctx = secrets.WithMasker(ctx, masker)
	if resolver, rerr := selectSecretResolver(ctx, opts); rerr != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", rerr.Error())
		return &Result{RunID: runID, Status: "failed", Error: rerr}, nil
	} else if resolver != nil {
		ctx = sparkwing.WithSecretResolver(ctx,
			secrets.NewCached(resolver, masker).AsResolver())
	} else if opts.SecretSource != nil {
		ctx = sparkwing.WithSecretResolver(ctx,
			secrets.NewCached(opts.SecretSource, masker).AsResolver())
	}
	pipeSec, err := sparkwingruntime.ResolvePipelineSecrets(ctx, reg, opts.PipelineYAML)
	if err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}
	if pipeSec != nil {
		ctx = sparkwingruntime.WithPipelineSecrets(ctx, pipeSec)
	}
	delegate := secrets.MaskingLogger(opts.Delegate, masker)

	if st := canonicalLocalStore(backends.State); st != nil {
		profileName := ""
		if opts.Profile != nil {
			profileName = opts.Profile.Name
		}
		consumerCtx, cancelConsumer := context.WithCancel(ctx)
		defer cancelConsumer()
		go runLocalTriggerLoop(consumerCtx, st, runID, profileName, parentTriggerRepoDir(), nil, wedgeBudget)
	}

	dispatchWaitTimeout := opts.DispatchWaitTimeout
	if dispatchWaitTimeout == 0 {
		dispatchWaitTimeout = DefaultDispatchWaitTimeout
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	var lease *runLease
	var leaseToken string
	skipDispatch := false
	if opts.Admission != nil {
		var outcome admitOutcome
		var admitErr error
		lease, outcome, admitErr = opts.Admission.admitRun(runCtx, backends, opts.Pipeline, runID, plan, opts.MaxParallel, cancelRun)
		if admitErr != nil {
			if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) {
				admitErr = cause
			}
			status := statusForRunError(admitErr)
			_ = backends.State.FinishRun(context.WithoutCancel(ctx), runID, status, admitErr.Error())
			return &Result{RunID: runID, Status: status, Error: admitErr}, nil
		}
		// safety: release only after FinishRun below, so the daemon's
		// orphan finalizer can never observe a still-running row.
		defer lease.release()
		if outcome == admitSkipped {
			skipDispatch = true
		} else {
			leaseToken = lease.token
		}
	}

	execStart := time.Now()
	var runErr error
	if !skipDispatch {
		runErr = dispatch(
			runCtx, backends, r, opts.Pipeline, runID, plan, delegate, opts.Debug, opts.RetryOf,
			opts.Full, masker, opts.MaxParallel, snapMeta, onlySkip,
			dispatchWaitTimeout, opts.Admission, leaseToken,
		)
	}

	finalStatus := statusForRunError(runErr)
	errMsg := ""
	if runErr != nil {
		errMsg = runErr.Error()
	}
	finishCtx := context.WithoutCancel(ctx)
	_ = backends.State.FinishRun(finishCtx, runID, finalStatus, errMsg)

	contentionNote := ""
	if lease != nil && !skipDispatch && opts.Admission != nil {
		contentionNote = opts.Admission.contentionAttribution(finishCtx, runID)
	}
	contended := contentionNote != ""
	if !skipDispatch {
		if st := canonicalLocalStore(backends.State); st != nil {
			charge := runCharge{}
			if lease != nil {
				charge = lease.charge
			}
			recordRunProfile(finishCtx, st, opts.Pipeline, runID, planPin(plan), planTopologyHash(plan.Nodes()), charge, contended, execStart, time.Now())
		}
	}
	if lease != nil && lease.driftWarning != "" && opts.Delegate != nil {
		opts.Delegate.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "warn",
			Event: "resource_pin_drift",
			Msg:   lease.driftWarning,
		})
	}
	if contended {
		if st := canonicalLocalStore(backends.State); st != nil && opts.Pipeline != "" {
			_ = st.RecordContention(finishCtx, opts.Pipeline)
		}
		if opts.Delegate != nil {
			opts.Delegate.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "info",
				Event: "run_contended",
				Msg:   contentionNote,
			})
		}
	}

	if opts.Delegate != nil {
		level := "info"
		if finalStatus != "success" {
			level = "error"
		}
		attrs := map[string]any{
			"run_id": runID,
			"status": finalStatus,
		}
		if runErr != nil {
			attrs["error"] = runErr.Error()
		}
		if runID != "" {
			hints := map[string]string{
				"status": "sparkwing runs status --run " + runID,
				"logs":   "sparkwing runs logs --run " + runID,
			}
			if finalStatus == "failed" {
				hints["retry"] = "sparkwing runs retry --failed --run " + runID
			}
			attrs["hints"] = hints
		}
		opts.Delegate.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: level,
			Event: "run_finish",
			Attrs: attrs,
		})
	}

	return &Result{RunID: runID, Status: finalStatus, Error: runErr}, nil
}

// runInterruptedError is the cancellation cause installed when the run
// process receives SIGINT or SIGTERM, so the run row finalizes as
// cancelled with the real reason instead of a generic failure.
type runInterruptedError struct {
	signal os.Signal
}

func (e *runInterruptedError) Error() string {
	return fmt.Sprintf("interrupted by %s", signalName(e.signal))
}

// signalName renders a signal for a terminal message, preferring the
// conventional uppercase name over Go's lowercase os.Signal.String() (so
// os.Interrupt reads "SIGINT", not the bare "interrupt").
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

// nodeSupersededError reports that a run's only non-passing nodes were
// superseded by a newer arrival under OnLimit:CancelOthers. It maps the
// run to cancelled -- the same terminal category as a plan-level
// eviction -- so a preempted run never reads as a job that broke.
type nodeSupersededError struct {
	nodes []string
}

func (e *nodeSupersededError) Error() string {
	return fmt.Sprintf("nodes superseded by a newer arrival: %v", e.nodes)
}

// runDaemonCanceledError reports that the admission daemon signalled this
// run to wind down in response to `sparkwing runs cancel`. It maps the run
// to cancelled, the same terminal category as an operator interrupt.
type runDaemonCanceledError struct {
	reason string
}

func (e *runDaemonCanceledError) Error() string { return e.reason }

// statusForRunError maps a run's terminal error to the stored status:
// an admission eviction (plan- or node-level), an operator interrupt, or
// a daemon cancel is cancelled, any other error failed, nil success.
func statusForRunError(err error) string {
	if err == nil {
		return "success"
	}
	var evicted *planAdmissionEvictedError
	var interrupted *runInterruptedError
	var superseded *nodeSupersededError
	var canceled *runDaemonCanceledError
	if errors.As(err, &evicted) || errors.As(err, &interrupted) ||
		errors.As(err, &superseded) || errors.As(err, &canceled) {
		return "cancelled"
	}
	return "failed"
}

// RunLocal opens the local store, wires LocalBackends, and runs.
// Defaults SecretSource to the laptop dotenv when nil.
func RunLocal(ctx context.Context, paths Paths, opts Options) (*Result, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, fmt.Errorf("ensure sparkwing root: %w", err)
	}
	if opts.SecretSource == nil {
		opts.SecretSource = secrets.NewDotenvSource("")
	}
	if opts.DefaultStateDB == "" {
		opts.DefaultStateDB = paths.StateDB()
	}
	ownsState := opts.State == nil
	if err := ApplyProfileBackendsWithMirror(ctx, &opts, opts.Profile, paths); err != nil {
		return nil, fmt.Errorf("profile backends: %w", err)
	}
	if opts.State == nil {
		return nil, fmt.Errorf("state backend: no store resolved (no spec configured and no default)")
	}
	var backends Backends
	var st *store.Store
	switch s := opts.State.(type) {
	case *store.Store:
		st = s
		if ownsState {
			defer func() { _ = st.Close() }()
		}
		backends = LocalBackends(paths, st, opts.ArtifactStore)
	case *s3state.Backend:
		if opts.LogStore == nil {
			return nil, fmt.Errorf("state backend: S3-only mode requires LogStore to be configured")
		}
		if ownsState {
			defer func() { _ = s.Close() }()
		}
		backends = S3Backends(opts.LogStore, s, opts.ArtifactStore)
	case *client.Client:
		var logsBackend LogBackend
		if opts.LogStore != nil {
			logsBackend = NewLogStoreBackend(opts.LogStore, nil)
		}
		backends = RemoteBackends(s, logsBackend, opts.ArtifactStore, nil, 0)
	default:
		return nil, fmt.Errorf("state backend: unrecognized implementation %T", opts.State)
	}
	if opts.MirrorLocal != nil {
		backends.State = newMirrorStateBackend(backends.State, opts.MirrorLocal, nil)
		defer func() { _ = opts.MirrorLocal.Close() }()
	}
	if opts.LogStore != nil {
		backends.Logs = NewLogStoreBackend(opts.LogStore, nil)
	}
	if opts.RunID == "" {
		opts.RunID = newRunID()
	}
	if err := paths.EnsureRunDir(opts.RunID); err != nil {
		return nil, fmt.Errorf("ensure run dir: %w", err)
	}
	envLog, envErr := newEnvelopeLogger(paths.EnvelopeLog(opts.RunID), opts.Delegate)
	if envErr == nil {
		opts.Delegate = envLog
		defer func() { _ = envLog.Close() }()
	}

	ctx, stopSignals := withInterruptCancel(ctx)
	defer stopSignals()

	res, runErr := Run(ctx, backends, opts)
	if st != nil && opts.ArtifactStore != nil && res != nil && res.RunID != "" {
		if err := DumpRunState(ctx, st, res.RunID, opts.ArtifactStore); err != nil {
			fmt.Fprintf(os.Stderr, "warn: state dump failed: %v\n", err)
		}
	}
	return res, runErr
}

// DumpRunState writes the run + node rows as NDJSON to
// runs/<runID>/state.ndjson. One line per record; run row first.
func DumpRunState(ctx context.Context, st *store.Store, runID string, art storage.ArtifactStore) error {
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("read run: %w", err)
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(map[string]any{"kind": "run", "data": run}); err != nil {
		return err
	}
	for _, n := range nodes {
		if err := enc.Encode(map[string]any{"kind": "node", "data": n}); err != nil {
			return err
		}
	}
	key := "runs/" + runID + "/state.ndjson"
	return art.Put(ctx, key, strings.NewReader(buf.String()))
}

// dispatch runs nodes in parallel where deps allow. Failed upstreams
// produce Cancelled downstreams (reason "upstream-failed"). OnFailure
// recoveries dispatch only when their parent fails.
func dispatch(
	ctx context.Context,
	backends Backends,
	r runner.Runner,
	pipeline string,
	runID string,
	plan *sparkwing.Plan,
	delegate sparkwing.Logger,
	debug DebugDirectives,
	retryOf string,
	full bool,
	masker *secrets.Masker,
	maxParallel int,
	snapMeta planSnapshotMeta,
	onlySkip map[string]string,
	dispatchWaitTimeout time.Duration,
	admission *LocalAdmission,
	leaseToken string,
) error {
	runStart := time.Now()
	dispatchCtx, cancelDispatch := context.WithCancelCause(ctx)
	defer cancelDispatch(nil)

	planRelease, planOutcome, planOutcomeGroup, perr := acquirePlanSlot(
		dispatchCtx, backends, runID, plan, admission != nil,
	)
	if perr != nil {
		return perr
	}
	switch planOutcome {
	case planCacheSkipped:
		return nil
	case planCacheFailed:
		return fmt.Errorf("plan concurrency group %q: slot full under OnLimit:Fail", planOutcomeGroup)
	case planCacheEvicted:
		return &planAdmissionEvictedError{groupName: planOutcomeGroup}
	}
	planReleaseOutcome := "success"
	defer func() { planRelease(planReleaseOutcome) }()

	state := newDispatchState(
		dispatchCtx, backends, r, pipeline, runID, plan, delegate, debug, retryOf,
		masker, maxParallel, admission, leaseToken,
	)
	state.pipelineRequires = snapMeta.PipelineRequires
	state.snapMeta = snapMeta
	state.onlySkip = onlySkip

	if retryOf != "" && !full {
		state.rehydrateFromRetry(dispatchCtx, retryOf)
	}

	seen := make(map[string]bool, len(plan.Nodes()))
	for _, n := range plan.Nodes() {
		state.scheduleNode(n)
		seen[n.ID()] = true
	}

	for _, n := range plan.Nodes() {
		rec := n.OnFailureNode()
		if rec == nil || seen[rec.ID()] {
			continue
		}
		_ = backends.State.CreateNode(ctx, store.Node{
			RunID:       runID,
			NodeID:      rec.ID(),
			Status:      "pending",
			Deps:        rec.DepIDs(),
			NeedsLabels: effectiveClaimLabels(rec, state.pipelineRequires),
		})
		state.scheduleNode(rec)
		seen[rec.ID()] = true
	}

	for _, exp := range plan.Expansions() {
		state.scheduleExpansion(exp)
	}

	if waitForDispatch(&state.wg, dispatchWaitTimeout) == dispatchWaitTimedOut {
		stuck := stuckNodeIDs(plan, state)
		stack := dumpAllGoroutineStacks(dispatchStackDumpBytes)
		summary, _ := json.Marshal(map[string]any{
			"timeout":     dispatchWaitTimeout.String(),
			"stuck_nodes": stuck,
			"stack_bytes": len(stack),
		})
		_ = backends.State.AppendEvent(ctx, runID, "", "dispatch_wait_timeout", summary)
		if delegate != nil {
			delegate.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "error",
				Event: "dispatch_wait_timeout",
				Msg:   stack,
				Attrs: map[string]any{
					"timeout_ms":  dispatchWaitTimeout.Milliseconds(),
					"stuck_nodes": stuck,
				},
			})
		}
		for _, nodeID := range stuck {
			state.markRunCancelled(nodeID)
		}
		planReleaseOutcome = "failed"
		return fmt.Errorf("dispatch_wait_timeout: %d node(s) did not terminate within %s: %v",
			len(stuck), dispatchWaitTimeout, stuck)
	}
	if cause := context.Cause(dispatchCtx); cause != nil &&
		!errors.Is(cause, context.Canceled) &&
		!errors.Is(cause, context.DeadlineExceeded) {
		planReleaseOutcome = "failed"
		return cause
	}

	var failed []string
	var superseded []string
	for _, n := range plan.Nodes() {
		oc, ok := state.getOutcome(n.ID())
		if !ok || oc.OK() {
			continue
		}
		if n.IsOptional() {
			continue
		}
		if oc == sparkwing.Superseded {
			superseded = append(superseded, n.ID())
			continue
		}
		failed = append(failed, n.ID())
	}

	emitRunSummary(delegate, plan, state, runStart, len(failed) == 0 && len(superseded) == 0)

	if len(failed) > 0 {
		planReleaseOutcome = "failed"
		return fmt.Errorf("nodes failed: %v", failed)
	}
	if len(superseded) > 0 {
		planReleaseOutcome = "superseded"
		return &nodeSupersededError{nodes: superseded}
	}
	return nil
}

// validatePlanModifiers warns on combinations that silently no-op.
func validatePlanModifiers(delegate sparkwing.Logger, plan *sparkwing.Plan) {
	if delegate == nil {
		return
	}
	for _, n := range plan.Nodes() {
		if n.IsInline() && len(n.RequiresLabels()) > 0 {
			delegate.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "warn",
				JobID: n.ID(),
				Event: "plan_warn",
				Msg:   "Inline() and Requires() are set on the same job -- Requires labels are ignored for inline execution",
				Attrs: map[string]any{
					"inline":      true,
					"requires":    n.RequiresLabels(),
					"ignored_key": "requires",
				},
			})
		}
	}
}

// emitRunStart sends a run_start record with follow-logs/status hints
// plus enough invocation context (args, flags, trigger-env keys, cwd)
// for an operator -- or an agent reading the JSONL stream -- to
// reproduce the run. Values for trigger env are deliberately omitted;
// only the names are surfaced so secret-bearing variables don't leak
// into the log/receipt.
// buildRunInvocation snapshots how this run was started: run-id,
// pipeline, args, flags, binary_source, cwd, hashes, hints, etc.
// The same map flows into BOTH the store.Run.Invocation column (so
// runs status / receipt / dashboards can answer "how was this
// started") and run_start.attrs (so the live JSONL stream agents
// read carries the same shape). Adding a new context field is a
// one-line edit here -- no schema migration, no separate emit-vs-
// store divergence.
// parentTriggerRepoDir returns the running pipeline's own working
// directory, whose .sparkwing/ tree lets a same-repo child trigger
// dispatch from the parent's already-compiled binary without a repo
// registry entry or a git identity. Empty when it can't be determined.
func parentTriggerRepoDir() string {
	if wd := sparkwing.WorkDir(); wd != "" {
		return wd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

func buildRunInvocation(opts Options, runID string) map[string]any {
	inv := map[string]any{
		"run_id":   runID,
		"pipeline": opts.Pipeline,
		"hints": map[string]string{
			"follow_logs": "sparkwing runs logs --run " + runID + " --follow",
			"status":      "sparkwing runs status --run " + runID,
		},
	}
	if src := os.Getenv("SPARKWING_BINARY_SOURCE"); src != "" {
		inv["binary_source"] = src
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
		inv["inputs_hash"] = hashCanonicalJSON(opts.Args)
	}
	if flags := buildRunFlags(opts); len(flags) > 0 {
		inv["flags"] = flags
	}
	if opts.Profile != nil && opts.ProfileChain != nil {
		state, logs, cache := opts.Profile.SurfaceStrings()
		inv["profile"] = map[string]any{
			"name":         opts.ProfileChain.Selected,
			"source":       string(opts.ProfileChain.Source),
			"mirror_local": opts.Profile.EffectiveMirrorLocal(),
		}
		inv["backends"] = map[string]any{
			"state": state,
			"logs":  logs,
			"cache": cache,
		}
	}
	inv["reproducer"] = buildReproducer(opts, runID)
	return inv
}

// emitRunStart sends a run_start record carrying the precomputed
// invocation snapshot. Caller passes the same map that was stored
// on store.Run.Invocation so the live stream and the persisted row
// agree byte-for-byte.
func emitRunStart(delegate sparkwing.Logger, invocation map[string]any) {
	if delegate == nil {
		return
	}
	delegate.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "run_start",
		Attrs: invocation,
	})
}

// buildRunFlags returns the operator-facing sparkwing flags that influenced
// this run. Empty/zero-valued fields are omitted so the resulting map
// reads as "what's non-default about this invocation". The shape
// matches the sparkwing CLI flag names so an agent can echo the map back
// into a re-invocation.
//
// --sw-allow is consumed by the sparkwing run dispatcher itself for
// the risk-label gate and never reaches Options. We pick up the
// authorized labels via SPARKWING_ALLOW (comma-separated) the
// dispatcher forwards specifically so they show up here.
func buildRunFlags(opts Options) map[string]any {
	flags := map[string]any{}
	if opts.RetryOf != "" {
		flags["retry_of"] = opts.RetryOf
	}
	if opts.Full {
		flags["full"] = true
	}
	if opts.DryRun {
		flags["dry_run"] = true
	}
	if opts.StartAt != "" {
		flags["start_at"] = opts.StartAt
	}
	if opts.StopAt != "" {
		flags["stop_at"] = opts.StopAt
	}
	if opts.MaxParallel > 0 {
		flags["max_parallel"] = opts.MaxParallel
	}
	if v := os.Getenv("SPARKWING_ALLOW"); v != "" {
		flags["allow"] = v
	}
	if v := os.Getenv("SPARKWING_REF"); v != "" {
		flags["ref"] = v
	}
	if os.Getenv("SPARKWING_NO_UPDATE") == "1" {
		flags["no_update"] = true
	}
	if v := os.Getenv("SPARKWING_PROFILE"); v != "" {
		flags["profile"] = v
	}
	if v := os.Getenv("SPARKWING_SECRETS_PROFILE"); v != "" {
		flags["secrets"] = v
	}
	if v := os.Getenv("SPARKWING_MODE"); v != "" {
		flags["mode"] = v
	}
	if os.Getenv("SPARKWING_LOG_LEVEL") == "debug" {
		flags["verbose"] = true
	}
	return flags
}

// hashCanonicalJSON returns sha256:<hex> of v's canonical JSON
// encoding. encoding/json sorts map keys, so map inputs hash
// deterministically across invocations. Mirrors the same algorithm
// the receipt package uses so an agent comparing the run_start's
// inputs_hash against the post-hoc receipt sees the same value.
func hashCanonicalJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(buf)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildReproducer assembles a `sparkwing run <pipeline> [flags] [args]` shell
// command that re-runs this invocation. Args use --key=value form so
// values containing spaces don't need extra escaping; consumers that
// need shell-quoting can run the result through their own escaper.
// The retry-of flag is included only when this run was itself a
// retry (i.e. opts.RetryOf is set); a fresh agent reproducing should
// pick whether to retry-of the failed run themselves.
func buildReproducer(opts Options, _ string) string {
	parts := []string{"sparkwing", "run", opts.Pipeline}
	flagKeys := make([]string, 0)
	flags := buildRunFlags(opts)
	for k := range flags {
		flagKeys = append(flagKeys, k)
	}
	sort.Strings(flagKeys)
	for _, k := range flagKeys {
		if k == "max_parallel" {
			continue
		}
		flagName := "--" + strings.ReplaceAll(k, "_", "-")
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
	argKeys := make([]string, 0, len(opts.Args))
	for k := range opts.Args {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	for _, k := range argKeys {
		parts = append(parts, "--"+k+"="+opts.Args[k])
	}
	return strings.Join(parts, " ")
}

// emitRunPlan sends a run_plan record carrying the DAG.
func emitRunPlan(delegate sparkwing.Logger, plan *sparkwing.Plan) {
	if delegate == nil {
		return
	}
	nodes := plan.Nodes()
	if len(nodes) == 0 {
		return
	}
	rows := make([]any, 0, len(nodes))
	for _, n := range nodes {
		row := map[string]any{
			"id":   n.ID(),
			"deps": n.DepIDs(),
		}
		if n.IsInline() {
			row["inline"] = true
		}
		if plan.IsDynamicNode(n.ID()) {
			row["dynamic"] = true
		}
		if n.IsApproval() {
			row["approval"] = true
		}
		if gs := plan.JobGroupNames(n.ID()); len(gs) > 0 {
			row["groups"] = gs
		}
		if srcs := plan.GroupSourceIDs(n.ID()); len(srcs) > 0 {
			row["group_deps"] = srcs
		}
		if w := n.Work(); w != nil {
			workSteps := w.Steps()
			if !(len(workSteps) == 1 && workSteps[0].ID() == "run") {
				groupByStep := map[string][]string{}
				for _, g := range w.Groups() {
					if g.Name() == "" {
						continue
					}
					for _, m := range g.Members() {
						groupByStep[m.ID()] = append(groupByStep[m.ID()], g.Name())
					}
				}
				stepRows := make([]map[string]any, 0, len(workSteps))
				for _, s := range workSteps {
					sr := map[string]any{"id": s.ID()}
					if deps := s.DepIDs(); len(deps) > 0 {
						sr["deps"] = deps
					}
					if gs := groupByStep[s.ID()]; len(gs) > 0 {
						sr["groups"] = gs
					}
					stepRows = append(stepRows, sr)
				}
				row["steps"] = stepRows
			}
		}
		rows = append(rows, row)
	}
	delegate.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "run_plan",
		Attrs: map[string]any{
			"nodes":     rows,
			"plan_hash": planTopologyHash(nodes),
		},
	})
}

// planTopologyHash hashes (id, sorted-deps) edges so two plans with
// the same DAG shape produce the same hash regardless of which run
// emitted them. Mirrors orchestrator/receipt's plan_hash so an agent
// can compare a live run_plan record against a post-hoc receipt.
func planTopologyHash(nodes []*sparkwing.JobNode) string {
	type edge struct {
		ID   string   `json:"id"`
		Deps []string `json:"deps"`
	}
	edges := make([]edge, 0, len(nodes))
	for _, n := range nodes {
		deps := append([]string(nil), n.DepIDs()...)
		sort.Strings(deps)
		edges = append(edges, edge{ID: n.ID(), Deps: deps})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return hashCanonicalJSON(edges)
}

// emitRunSummary sends an end-of-run summary record.
func emitRunSummary(delegate sparkwing.Logger, plan *sparkwing.Plan, state *dispatchState, runStart time.Time, ok bool) {
	if delegate == nil {
		return
	}
	nodeSummaries := map[string]string{}
	stepSummaries := map[string]map[string]string{}
	if state.backends.State != nil {
		if steps, err := state.backends.State.ListNodeSteps(state.ctx, state.runID); err == nil {
			for _, s := range steps {
				if s.Summary == "" {
					continue
				}
				if stepSummaries[s.NodeID] == nil {
					stepSummaries[s.NodeID] = map[string]string{}
				}
				stepSummaries[s.NodeID][s.StepID] = s.Summary
			}
		}
		for _, n := range plan.Nodes() {
			row, err := state.backends.State.GetNode(state.ctx, state.runID, n.ID())
			if err == nil && row != nil && row.Summary != "" {
				nodeSummaries[n.ID()] = row.Summary
			}
		}
	}
	nodes := plan.Nodes()
	rows := make([]any, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	appendRow := func(id, outcome string) {
		state.mu.Lock()
		dur := state.durations[id]
		errMsg := state.errors[id]
		state.mu.Unlock()
		row := map[string]any{
			"id":          id,
			"outcome":     outcome,
			"duration_ms": dur.Milliseconds(),
		}
		if errMsg != "" {
			row["error"] = errMsg
		}
		if plan.IsDynamicNode(id) {
			row["dynamic"] = true
		}
		if md := nodeSummaries[id]; md != "" {
			row["summary"] = md
		}
		if perStep := stepSummaries[id]; len(perStep) > 0 {
			steps := make([]any, 0, len(perStep))
			for stepID, md := range perStep {
				steps = append(steps, map[string]any{"step_id": stepID, "summary": md})
			}
			row["step_summaries"] = steps
		}
		rows = append(rows, row)
		seen[id] = true
	}
	for _, n := range nodes {
		if seen[n.ID()] {
			continue
		}
		oc, have := state.getOutcome(n.ID())
		outcome := "unknown"
		if have {
			outcome = string(oc)
		}
		appendRow(n.ID(), outcome)
		if rec := n.OnFailureNode(); rec != nil && !seen[rec.ID()] {
			if recOC, recHave := state.getOutcome(rec.ID()); recHave {
				appendRow(rec.ID(), string(recOC))
			}
		}
	}
	status := "success"
	if !ok {
		status = "failed"
	}
	delegate.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "run_summary",
		Attrs: map[string]any{
			"status":      status,
			"duration_ms": time.Since(runStart).Milliseconds(),
			"nodes":       rows,
		},
	})
}

// dispatchState holds shared coordination state for one run.
type dispatchState struct {
	ctx              context.Context
	resolverCtx      context.Context
	backends         Backends
	runner           runner.Runner
	runID            string
	plan             *sparkwing.Plan
	delegate         sparkwing.Logger
	pipelineRequires []string // pipeline-level label requirements unioned into every node's effective requires

	mu        sync.Mutex
	doneCh    map[string]chan struct{} // per-node completion signal
	outputs   map[string]any           // per-node typed output (in-process runner)
	outputsJS map[string][]byte        // per-node raw JSON output (cluster runner)
	outcomes  map[string]sparkwing.Outcome
	errors    map[string]string             // per-node error message, set when runner.Result.Err is non-nil
	failures  map[string]sparkwing.Failure  // per-node failure (stage + err), set when a node fails
	starts    map[string]time.Time          // per-node wall-clock start, stamped at runOneNode entry
	durations map[string]time.Duration      // per-node wall-clock duration, computed when outcome is recorded
	claimedBy map[string]string             // recoveryID -> parentID (OnFailure)
	scheduled map[string]*sparkwing.JobNode // every node handed to scheduleNode, including runtime-scheduled dynamic and recovery nodes

	// inlineRunner routes Node.IsInline nodes regardless of the
	// configured Options.Runner so glue work skips pod spin-up.
	inlineRunner runner.Runner
	debug        DebugDirectives

	// retryOf chains retry_of across nested spawns when this run is
	// itself a retry.
	retryOf string

	// masker is always non-nil; zero-value is a fast no-op.
	masker *secrets.Masker

	// sem caps concurrent RunNode calls; nil = unbounded. Inline
	// nodes bypass the cap.
	sem chan struct{}

	// snapMeta carries the run-level SecretsField and PipelineRequires
	// that the cluster pod reads back to re-resolve PipelineSecrets.
	// Captured at run start so the mid-run snapshot re-marshal (after
	// dynamic expansion) preserves these fields.
	snapMeta planSnapshotMeta

	// onlySkip captures the --only job-level filter. Nodes outside the
	// matched set (plus their transitive Needs() ancestors) are skipped
	// at dispatch entry. Empty map = no filter.
	onlySkip map[string]string

	wg sync.WaitGroup
}

func newDispatchState(
	ctx context.Context,
	backends Backends,
	r runner.Runner,
	pipeline string,
	runID string,
	plan *sparkwing.Plan,
	delegate sparkwing.Logger,
	debug DebugDirectives,
	retryOf string,
	masker *secrets.Masker,
	maxParallel int,
	admission *LocalAdmission,
	leaseToken string,
) *dispatchState {
	if masker == nil {
		masker = secrets.NewMasker()
	}
	var sem chan struct{}
	if maxParallel > 0 && admission == nil {
		sem = make(chan struct{}, maxParallel)
	}
	s := &dispatchState{
		sem:       sem,
		ctx:       ctx,
		backends:  backends,
		runner:    r,
		runID:     runID,
		plan:      plan,
		delegate:  delegate,
		retryOf:   retryOf,
		masker:    masker,
		doneCh:    map[string]chan struct{}{},
		outputs:   map[string]any{},
		outputsJS: map[string][]byte{},
		outcomes:  map[string]sparkwing.Outcome{},
		errors:    map[string]string{},
		failures:  map[string]sparkwing.Failure{},
		starts:    map[string]time.Time{},
		durations: map[string]time.Duration{},
		claimedBy: map[string]string{},
		scheduled: map[string]*sparkwing.JobNode{},
		debug:     debug,
	}
	if ipr, ok := r.(*InProcessRunner); ok {
		s.inlineRunner = ipr
	} else {
		s.inlineRunner = NewInProcessRunner(backends)
	}
	for _, n := range plan.Nodes() {
		if rec := n.OnFailureNode(); rec != nil {
			s.claimedBy[rec.ID()] = n.ID()
		}
	}
	if delegate != nil {
		s.resolverCtx = sparkwingruntime.WithLogger(ctx, delegate)
	} else {
		s.resolverCtx = ctx
	}
	s.resolverCtx = withLocalAdmission(s.resolverCtx, admission, leaseToken, pipeline, planPin(plan), maxParallel)
	s.resolverCtx = sparkwingruntime.WithResolver(s.resolverCtx, s.resolve)
	s.resolverCtx = sparkwingruntime.WithJSONResolver(s.resolverCtx, s.resolveJSON)
	s.resolverCtx = sparkwingruntime.WithPipelineResolver(s.resolverCtx, s.pipelineRef())
	s.resolverCtx = sparkwingruntime.WithPipelineAwaiter(s.resolverCtx, s.pipelineAwaiter())
	if in := plan.Inputs(); in != nil {
		s.resolverCtx = sparkwingruntime.WithInputs(s.resolverCtx, in)
	}
	if ra := plan.ResolvedArgs(); ra != nil {
		s.resolverCtx = sparkwingruntime.WithResolvedArgs(s.resolverCtx, ra)
	}
	return s
}

// pipelineAwaiter enqueues a child trigger, polls until terminal,
// returns the target node's output bytes.
func (s *dispatchState) pipelineAwaiter() sparkwing.PipelineAwaiter {
	return sparkwing.PipelineAwaiterFunc(func(ctx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
		currentNode := sparkwing.NodeFromContext(ctx)

		var childRetryOf string
		if s.retryOf != "" && currentNode != "" {
			id, ferr := s.backends.State.FindSpawnedChildTriggerID(ctx, s.retryOf, currentNode, req.Pipeline)
			if ferr != nil {
				sparkwing.Warn(ctx, "find prior spawned child for retry chain: %v", ferr)
			} else {
				childRetryOf = id
			}
		}

		childRunID, err := enqueueTriggerWithEnv(ctx, s.backends.State,
			req.Pipeline, req.Args, s.runID, currentNode, childRetryOf,
			"await-pipeline", "", req.Repo, req.Branch,
			leaseTriggerEnv(ctx),
		)
		if err != nil {
			return nil, fmt.Errorf("enqueue trigger: %w", err)
		}

		sparkwing.Info(ctx,
			"spawned child run %s (pipeline=%s%s)",
			childRunID, req.Pipeline, repoSuffix(req.Repo))

		startedAt := time.Now()
		emitChildFinish := func(status, errMsg string) {
			if currentNode == "" {
				return
			}
			attrs := map[string]any{
				"child_run_id": childRunID,
				"pipeline":     req.Pipeline,
				"status":       status,
				"duration_ms":  time.Since(startedAt).Milliseconds(),
			}
			if errMsg != "" {
				attrs["error"] = errMsg
			}
			payload, _ := json.Marshal(attrs)
			_ = s.backends.State.AppendEvent(context.WithoutCancel(ctx), s.runID, currentNode, "child_run_finish", payload)
		}

		if currentNode != "" {
			payload, _ := json.Marshal(map[string]any{
				"child_run_id":    childRunID,
				"pipeline":        req.Pipeline,
				"node_id":         req.NodeID,
				"args":            req.Args,
				"timeout_seconds": int64(req.Timeout.Seconds()),
			})
			if ev := s.backends.State.AppendEvent(ctx, s.runID, currentNode,
				"child_run_start", payload); ev != nil {
				sparkwing.Warn(ctx, "child_run_start audit event append failed: %v", ev)
			}
		}

		pollCtx := ctx
		parentCtx := nodeParentContextFromContext(ctx)
		if req.Timeout > 0 {
			var cancel context.CancelFunc
			pollCtx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}
		timeoutPausedForAdmission := false
		timeoutAdjustedForAdmission := false
		var admissionStatusErr error
		nodeTimeout := nodeTimeoutControllerFromContext(ctx)
		var admissionMu sync.Mutex
		updateTimeoutForAdmission := func(statusCtx context.Context) bool {
			if req.Timeout > 0 || nodeTimeout == nil || nodeTimeoutDurationFromContext(ctx) <= 0 {
				return false
			}
			if statusCtx.Err() != nil {
				return false
			}
			la, _ := localAdmissionFromContext(ctx)
			admission, statusErr := childAdmissionStatus(statusCtx, s.backends.State, s.backends.Concurrency, la, childRunID)
			admissionMu.Lock()
			defer admissionMu.Unlock()
			if timeoutAdjustedForAdmission {
				return false
			}
			if statusErr != nil {
				if timeoutPausedForAdmission {
					admissionStatusErr = statusErr
				}
				return false
			}
			switch admission.Status {
			case childPlanAdmissionQueued:
				if timeoutPausedForAdmission {
					return true
				}
				if nodeTimeout.pauseAt(admission.QueuedAt) {
					timeoutPausedForAdmission = true
					sparkwing.Info(ctx,
						"child %s [%s] is queued for plan admission; pausing parent node timeout until admission",
						childRunID, req.Pipeline)
					return true
				}
			case childPlanAdmissionAdmitted:
				if timeoutPausedForAdmission {
					if nodeTimeout.resumeAt(admission.AdmittedAt) {
						timeoutPausedForAdmission = false
						timeoutAdjustedForAdmission = true
						sparkwing.Info(ctx,
							"child %s [%s] left plan admission; parent node timeout resumed",
							childRunID, req.Pipeline)
						return true
					}
					return false
				}
				if admission.QueuedAt.IsZero() || admission.AdmittedAt.IsZero() {
					return false
				}
				if nodeTimeout.accountCompletedAdmission(admission.QueuedAt, admission.AdmittedAt) {
					timeoutAdjustedForAdmission = true
					sparkwing.Info(ctx,
						"child %s [%s] completed plan admission; parent node timeout adjusted",
						childRunID, req.Pipeline)
					return true
				}
			}
			return false
		}
		admissionPauseActive := func() bool {
			admissionMu.Lock()
			defer admissionMu.Unlock()
			return timeoutPausedForAdmission
		}
		currentAdmissionStatusErr := func() error {
			admissionMu.Lock()
			defer admissionMu.Unlock()
			return admissionStatusErr
		}
		admissionDeadlineHandled := func() bool {
			inspectCtx, cancel := context.WithTimeout(parentCtx, childAdmissionInspectorTimeout)
			defer cancel()
			return updateTimeoutForAdmission(inspectCtx)
		}
		if nodeTimeout != nil && req.Timeout == 0 {
			clearInspector := nodeTimeout.setDeadlineInspector(admissionDeadlineHandled)
			defer clearInspector()
		}

		wedge, err := newStoreWedgeGuardFromEnv()
		if err != nil {
			return nil, err
		}
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		lastStatus := "pending"
		for {
			updateTimeoutForAdmission(parentCtx)
			if statusErr := currentAdmissionStatusErr(); statusErr != nil {
				emitChildFinish("failed", statusErr.Error())
				return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
			}
			run, err := s.backends.State.GetRun(pollCtx, childRunID)
			if err != nil {
				// safety: ErrNotFound is a healthy store answer here -- the
				// child's runs row appears only once a consumer claims and
				// starts it, so a queued or still-compiling child must not
				// count toward the wedge budget.
				if errors.Is(err, store.ErrNotFound) {
					wedge.success()
				} else if terminal := wedge.fail(fmt.Sprintf("waiting for child run %s", childRunID), err); terminal != nil {
					emitChildFinish("failed", terminal.Error())
					return nil, terminal
				}
			} else {
				wedge.success()
				lastStatus = run.Status
				switch run.Status {
				case "success":
					updateTimeoutForAdmission(parentCtx)
					if err := pollCtx.Err(); err != nil {
						emitChildFinish("timeout", err.Error())
						return nil, fmt.Errorf("waiting for child %s: %w", childRunID, err)
					}
					if deadline, ok := pollCtx.Deadline(); ok && time.Now().After(deadline) {
						emitChildFinish("timeout", context.DeadlineExceeded.Error())
						return nil, fmt.Errorf("waiting for child %s: %w", childRunID, context.DeadlineExceeded)
					}
					emitChildFinish("success", "")
					if req.NodeID == "" {
						return &sparkwing.ResolvedPipelineRef{RunID: childRunID}, nil
					}
					data, oerr := s.backends.State.GetNodeOutput(pollCtx, childRunID, req.NodeID)
					if oerr != nil {
						return nil, fmt.Errorf("get child %s/%s output: %w", childRunID, req.NodeID, oerr)
					}
					return &sparkwing.ResolvedPipelineRef{RunID: childRunID, Data: data}, nil
				case "failed":
					emitChildFinish("failed", run.Error)
					return nil, fmt.Errorf("child run %s failed: %s", childRunID, run.Error)
				case "cancelled":
					emitChildFinish("cancelled", "")
					return nil, fmt.Errorf("child run %s was cancelled", childRunID)
				}
			}
			updateTimeoutForAdmission(parentCtx)
			if statusErr := currentAdmissionStatusErr(); statusErr != nil {
				emitChildFinish("failed", statusErr.Error())
				return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
			}
			select {
			case <-pollCtx.Done():
				updateTimeoutForAdmission(parentCtx)
				emitChildFinish("timeout", pollCtx.Err().Error())
				return nil, fmt.Errorf("waiting for child %s: %w", childRunID, pollCtx.Err())
			case <-heartbeat.C:
				sparkwing.Info(ctx,
					"still waiting on child %s [%s] (status=%s, elapsed=%s)",
					childRunID, req.Pipeline, lastStatus,
					time.Since(startedAt).Round(time.Second))
			case <-time.After(childAwaitPollInterval(pollCtx, admissionPauseActive())):
			}
		}
	})
}

// repoSuffix returns " repo=<slug>" or "".
func repoSuffix(repo string) string {
	if repo == "" {
		return ""
	}
	return " repo=" + repo
}

// pipelineRef returns a PipelineResolver bound to this dispatchState.
func (s *dispatchState) pipelineRef() sparkwing.PipelineResolver {
	return sparkwing.PipelineResolverFunc(func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*sparkwing.ResolvedPipelineRef, error) {
		run, err := s.backends.State.GetLatestRun(ctx, pipeline, []string{"success"}, maxAge)
		if err != nil {
			return nil, fmt.Errorf("no matching run for pipeline %q (maxAge=%s): %w", pipeline, maxAge, err)
		}
		data, err := s.backends.State.GetNodeOutput(ctx, run.ID, nodeID)
		if err != nil {
			return nil, fmt.Errorf("get node %s/%s output: %w", run.ID, nodeID, err)
		}
		currentNode := sparkwing.NodeFromContext(ctx)
		if currentNode != "" {
			payload, _ := json.Marshal(map[string]any{
				"pipeline":        pipeline,
				"node_id":         nodeID,
				"source_run_id":   run.ID,
				"max_age_seconds": int64(maxAge.Seconds()),
				"source_finished": run.FinishedAt,
			})
			if evErr := s.backends.State.AppendEvent(ctx, s.runID, currentNode,
				"pipeline_ref_resolved", payload); evErr != nil {
				sparkwing.Warn(ctx,
					"pipeline_ref audit event append failed: %v", evErr)
			}
		}
		return &sparkwing.ResolvedPipelineRef{RunID: run.ID, Data: data}, nil
	})
}

// rehydrateFromRetry pre-seeds passed nodes from priorRunID so
// runOneNode short-circuits them. Runs before scheduleNode, so no
// locks needed. Non-success nodes dispatch normally.
func (s *dispatchState) rehydrateFromRetry(ctx context.Context, priorRunID string) {
	successOutcome := string(sparkwing.Success)
	for _, n := range s.plan.Nodes() {
		prior, err := s.backends.State.GetNode(ctx, priorRunID, n.ID())
		if err != nil || prior == nil {
			continue
		}
		if prior.Outcome != successOutcome {
			continue
		}
		s.outputsJS[n.ID()] = prior.Output
		s.outcomes[n.ID()] = sparkwing.Success
		_ = s.backends.State.FinishNode(ctx, s.runID, n.ID(),
			successOutcome, "", prior.Output)
		payload, _ := json.Marshal(map[string]any{
			"prior_run_id": priorRunID,
		})
		_ = s.backends.State.AppendEvent(ctx, s.runID, n.ID(),
			"node_skipped_from_retry", payload)
	}
}

func (s *dispatchState) resolve(id string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.outputs[id]
	return v, ok
}

func (s *dispatchState) resolveJSON(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.outputsJS[id]
	return v, ok
}

func (s *dispatchState) setOutput(id string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, isBytes := v.([]byte); isBytes {
		s.outputsJS[id] = b
		return
	}
	s.outputs[id] = v
}

func (s *dispatchState) setOutcome(id string, o sparkwing.Outcome) {
	s.mu.Lock()
	s.outcomes[id] = o
	if started, ok := s.starts[id]; ok {
		s.durations[id] = time.Since(started)
	}
	s.mu.Unlock()
}

// setError records a node's flattened error message.
func (s *dispatchState) setError(id, msg string) {
	s.mu.Lock()
	s.errors[id] = msg
	s.mu.Unlock()
}

// setFailure records a node's structured failure (stage + error), read
// back when dispatching the node's OnFailure recovery.
func (s *dispatchState) setFailure(id string, f sparkwing.Failure) {
	s.mu.Lock()
	s.failures[id] = f
	s.mu.Unlock()
}

// getFailure returns the recorded failure for a node, or the zero
// Failure if none was recorded.
func (s *dispatchState) getFailure(id string) sparkwing.Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[id]
}

// failureFrom attributes a node failure to its lifecycle stage using the
// store's serializable failure reason rather than the Go error type. The
// reason ([store.FailureVerify] etc.) is written by whichever runner ran
// the node and survives a remote runner's process boundary, where the
// typed *sparkwing.VerifyError does not. store.FailureVerify maps to
// StageVerify; anything else is an action-stage failure. When the
// in-process VerifyError wrapper is present it is unwrapped so recovery
// sees the check's own error rather than the envelope.
func failureFrom(reason string, err error) sparkwing.Failure {
	stage := sparkwing.StageAction
	if reason == store.FailureVerify {
		stage = sparkwing.StageVerify
	}
	var ve *sparkwing.VerifyError
	if errors.As(err, &ve) {
		err = ve.Err
	}
	return sparkwing.Failure{Stage: stage, Err: err}
}

// markStarted stamps the wall-clock time runOneNode begins real work.
func (s *dispatchState) markStarted(id string) {
	s.mu.Lock()
	if _, ok := s.starts[id]; !ok {
		s.starts[id] = time.Now()
	}
	s.mu.Unlock()
}

func (s *dispatchState) getOutcome(id string) (sparkwing.Outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[id]
	return o, ok
}

// ensureDoneCh creates if absent and returns the completion channel.
func (s *dispatchState) ensureDoneCh(id string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.doneCh[id]; ok {
		return ch
	}
	ch := make(chan struct{})
	s.doneCh[id] = ch
	return ch
}

func (s *dispatchState) lookupDoneCh(id string) (chan struct{}, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.doneCh[id]
	return ch, ok
}

// scheduleNode spawns the per-node dispatch goroutine.
func (s *dispatchState) scheduleNode(node *sparkwing.JobNode) {
	s.mu.Lock()
	s.scheduled[node.ID()] = node
	s.mu.Unlock()
	done := s.ensureDoneCh(node.ID())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		s.runOneNode(node)
	}()
}

// scheduleExpansion waits for source, runs generator, inserts and
// dispatches children, signals group.
func (s *dispatchState) scheduleExpansion(exp sparkwing.Expansion) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runOneExpansion(exp)
	}()
}

func (s *dispatchState) runOneExpansion(exp sparkwing.Expansion) {
	sourceCh := s.ensureDoneCh(exp.Source.ID())
	select {
	case <-sourceCh:
	case <-s.resolverCtx.Done():
		sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, nil, fmt.Errorf("ctx cancelled before expansion"))
		return
	}

	oc, _ := s.getOutcome(exp.Source.ID())
	if !oc.OK() {
		sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, nil, fmt.Errorf("expansion source %q did not succeed (outcome=%s)", exp.Source.ID(), oc))
		return
	}

	children, err := s.invokeGenerator(exp)
	if err != nil {
		sparkwing.LoggerFromContext(s.resolverCtx).Log("error",
			fmt.Sprintf("ExpandFrom(%s) failed: %v", exp.Source.ID(), err))
		sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, nil, err)
		return
	}

	if err := sparkwing.RuntimePlumbing.Fns.PlanInsertExpanded(s.plan, exp.Source, children); err != nil {
		sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, nil, err)
		return
	}
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, exp.Source.ID(), "expansion_generated",
		fmt.Appendf(nil, "%d children", len(children)))

	for _, child := range children {
		if err := s.backends.State.CreateNode(s.ctx, store.Node{
			RunID:       s.runID,
			NodeID:      child.ID(),
			Status:      "pending",
			Deps:        child.DepIDs(),
			NeedsLabels: effectiveClaimLabels(child, s.pipelineRequires),
		}); err != nil {
			sparkwing.LoggerFromContext(s.resolverCtx).Log("error",
				fmt.Sprintf("ExpandFrom(%s): store child %s: %v", exp.Source.ID(), child.ID(), err))
		}
		s.scheduleNode(child)
	}
	if snap, merr := marshalPlanSnapshot(s.plan, sparkwing.RunContext{Pipeline: "", RunID: s.runID}, s.snapMeta); merr == nil {
		_ = s.backends.State.UpdatePlanSnapshot(s.ctx, s.runID, snap)
	}

	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID()
	}
	for _, waiter := range s.plan.Nodes() {
		for _, grp := range waiter.NeedsGroups() {
			if grp != exp.Group {
				continue
			}
			merged := append(append([]string{}, waiter.DepIDs()...), childIDs...)
			_ = s.backends.State.UpdateNodeDeps(s.ctx, s.runID, waiter.ID(), merged)
		}
	}

	sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, children, nil)
}

// invokeGenerator runs the user closure under panic recovery.
func (s *dispatchState) invokeGenerator(exp sparkwing.Expansion) (out []*sparkwing.JobNode, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	out = exp.Gen(s.resolverCtx)
	return out, nil
}

// runOneNode coordinates per-node dispatch: deps/groups/OnFailure
// waits, dispatch decision, hand-off to the Runner.
func (s *dispatchState) runOneNode(node *sparkwing.JobNode) {
	if _, prerendered := s.getOutcome(node.ID()); prerendered {
		return
	}
	if _, claimed := s.claimedBy[node.ID()]; !claimed {
		if reason, ok := s.onlySkip[node.ID()]; ok {
			s.markSkipped(node.ID(), reason)
			return
		}
	}
	if parentID, claimed := s.claimedBy[node.ID()]; claimed {
		parentCh, ok := s.lookupDoneCh(parentID)
		if !ok {
			s.markFailed(node.ID(), fmt.Errorf("OnFailure parent %q not found", parentID))
			return
		}
		select {
		case <-parentCh:
		case <-s.resolverCtx.Done():
			s.markCancelled(node.ID(), "ctx-cancelled")
			return
		}
		parentOutcome, _ := s.getOutcome(parentID)
		if parentOutcome != sparkwing.Failed {
			s.markSkipped(node.ID(), fmt.Sprintf("parent %q did not fail (outcome=%s)", parentID, parentOutcome))
			return
		}
		s.markStarted(node.ID())
		res := s.invokeRecoveryRunner(node, s.getFailure(parentID))
		s.applyResult(node.ID(), res)
		return
	}

	var groupMemberIDs []string
	for _, grp := range node.NeedsGroups() {
		select {
		case <-grp.Ready():
		case <-s.resolverCtx.Done():
			s.markCancelled(node.ID(), "ctx-cancelled")
			return
		}
		if grp.Err() != nil {
			s.markCancelled(node.ID(), fmt.Sprintf("expansion failed: %v", grp.Err()))
			return
		}
		for _, m := range grp.Members() {
			groupMemberIDs = append(groupMemberIDs, m.ID())
		}
	}

	hardDeps := node.DepIDs()
	optDeps := []string{}
	for _, id := range node.OptionalDepIDs() {
		if _, ok := s.lookupDoneCh(id); ok {
			optDeps = append(optDeps, id)
		}
	}
	allDeps := append(append(append([]string{}, hardDeps...), optDeps...), groupMemberIDs...)

	for _, depID := range allDeps {
		ch, ok := s.lookupDoneCh(depID)
		if !ok {
			s.markFailed(node.ID(), fmt.Errorf("unknown dependency %q", depID))
			return
		}
		select {
		case <-ch:
		case <-s.resolverCtx.Done():
			s.markCancelled(node.ID(), "ctx-cancelled")
			return
		}
	}

	for _, depID := range allDeps {
		oc, ok := s.getOutcome(depID)
		if !ok || oc.OK() {
			continue
		}
		upstream := s.plan.Job(depID)
		if upstream != nil && upstream.IsContinueOnError() {
			continue
		}
		s.markCancelled(node.ID(), "upstream-failed")
		return
	}

	if s.debug.pauseBefore(node.ID()) {
		if cancelled := s.doPause(node.ID(), store.PauseReasonBefore); cancelled {
			s.markCancelled(node.ID(), "ctx-cancelled")
			return
		}
	}

	if node.IsApproval() {
		if reason, skip := evalSkipPredicates(s.resolverCtx, node); skip {
			s.markSkipped(node.ID(), reason)
			return
		}
		s.markStarted(node.ID())
		res := s.runApprovalGate(node)
		s.applyResult(node.ID(), res)
		return
	}

	activeRunner := s.runner
	if node.IsInline() {
		activeRunner = s.inlineRunner
	}

	if labels := node.WhenRunnerLabels(); len(labels) > 0 {
		if adv, ok := activeRunner.(runner.LabelAdvertiser); ok {
			if !sparkwingruntime.MatchLabels(labels, adv.AdvertisedLabels()) {
				s.markSkipped(node.ID(),
					fmt.Sprintf("WhenRunner labels %v not satisfied by active runner %v",
						labels, adv.AdvertisedLabels()))
				return
			}
		}
	}

	s.markStarted(node.ID())
	runnerCtx := sparkwingruntime.WithSpawnHandler(s.resolverCtx, s.newSpawnHandler(node.ID()))
	runnerCtx = sparkwingruntime.WithRunner(runnerCtx, runnerInfoFor(activeRunner))

	retryCfg := node.RetryConfig()
	var autoAttempts int
	var autoBackoff time.Duration
	if retryCfg.Auto {
		autoAttempts = retryCfg.Attempts
		autoBackoff = retryCfg.Backoff
	}
	totalAutoAttempts := autoAttempts + 1
	var res runner.Result
	for autoAttempt := range totalAutoAttempts {
		if autoAttempt > 0 {
			wait := scaledBackoff(autoBackoff, autoAttempt)
			msg := fmt.Sprintf("auto-retry dispatch %d/%d", autoAttempt+1, totalAutoAttempts)
			if wait > 0 {
				msg = fmt.Sprintf("auto-retry dispatch %d/%d after %s", autoAttempt+1, totalAutoAttempts, wait)
			}
			sparkwing.LoggerFromContext(s.resolverCtx).Log("info", msg)
			_ = s.backends.State.AppendEvent(s.ctx, s.runID, node.ID(), "node_auto_retry",
				fmt.Appendf(nil, "dispatch %d/%d", autoAttempt+1, totalAutoAttempts))
			if wait > 0 {
				select {
				case <-time.After(wait):
				case <-s.resolverCtx.Done():
					s.applyResult(node.ID(), runner.Result{Outcome: sparkwing.Cancelled})
					return
				}
			}
		}

		res = s.runWithCap(node, func(slot *workerSlot) runner.Result {
			return activeRunner.RunNode(runnerCtx, runner.Request{
				RunID:               s.runID,
				NodeID:              node.ID(),
				Pipeline:            localPipelineFromContext(runnerCtx),
				Node:                node,
				Delegate:            s.delegate,
				ReleaseWorkerSlot:   slot.release,
				ReacquireWorkerSlot: slot.reacquire,
			})
		})

		if res.Outcome != sparkwing.Failed || res.Err == nil {
			break
		}
		if autoAttempt < autoAttempts {
			sparkwing.LoggerFromContext(s.resolverCtx).Log("warn",
				fmt.Sprintf("node %s auto-retry dispatch %d/%d failed: %v",
					node.ID(), autoAttempt+1, totalAutoAttempts, res.Err))
		}
	}

	if res.Outcome == sparkwing.Failed && s.resolverCtx.Err() != nil && canceledByRun(res.Err) {
		s.markRunCancelled(node.ID())
		return
	}

	pauseReason := ""
	if s.debug.pauseAfter(node.ID()) {
		pauseReason = store.PauseReasonAfter
	} else if s.debug.PauseOnFailure && res.Outcome == sparkwing.Failed && res.Err != nil {
		pauseReason = store.PauseReasonOnFailure
	}
	if pauseReason != "" {
		if cancelled := s.doPause(node.ID(), pauseReason); cancelled {
			s.applyResult(node.ID(), runner.Result{Outcome: sparkwing.Cancelled})
			return
		}
	}

	s.applyResult(node.ID(), res)
}

// defaultPauseTimeout caps a paused node; SPARKWING_PAUSE_TIMEOUT
// (Go duration) overrides.
var defaultPauseTimeout = 30 * time.Minute

func pauseTimeout() time.Duration {
	if v := os.Getenv("SPARKWING_PAUSE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultPauseTimeout
}

// doPause writes the paused row, flips node.status to 'paused', emits
// an audit event, then polls the debug_pauses table until released or
// the deadline fires. Returns true when the resolver ctx is cancelled
// mid-pause so the caller can mark the node cancelled; false on
// normal release (manual or timeout).
func (s *dispatchState) doPause(nodeID, reason string) bool {
	now := time.Now()
	timeout := pauseTimeout()
	pause := store.DebugPause{
		RunID:     s.runID,
		NodeID:    nodeID,
		Reason:    reason,
		PausedAt:  now,
		ExpiresAt: now.Add(timeout),
	}
	if err := s.backends.State.CreateDebugPause(s.ctx, pause); err != nil {
		sparkwing.LoggerFromContext(s.resolverCtx).Log("error",
			fmt.Sprintf("debug pause %s/%s: create row: %v", nodeID, reason, err))
		return false
	}
	_ = s.backends.State.SetNodeStatus(s.ctx, s.runID, nodeID, sparkwing.Paused)
	payload, _ := json.Marshal(map[string]any{
		"reason":     reason,
		"expires_at": pause.ExpiresAt,
	})
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_paused", payload)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := s.backends.State.GetActiveDebugPause(s.ctx, s.runID, nodeID)
		if err != nil {
			break
		}
		if p == nil || p.ReleasedAt != nil {
			break
		}
		if time.Now().After(p.ExpiresAt) {
			_ = s.backends.State.ReleaseDebugPause(s.ctx, s.runID, nodeID,
				"orchestrator", store.PauseReleaseTimeout)
			_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID,
				"node_paused_timeout", nil)
			break
		}
		select {
		case <-s.resolverCtx.Done():
			return true
		case <-ticker.C:
		}
	}
	// safety: "pending" is safe here; StartNode promotes it and FinishNode overwrites it.
	_ = s.backends.State.SetNodeStatus(s.ctx, s.runID, nodeID, "pending")
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_resumed", nil)
	return false
}

// applyResult mirrors the runner's terminal outcome into in-memory
// state for downstream coordination.
func (s *dispatchState) applyResult(nodeID string, res runner.Result) {
	if res.Output != nil {
		s.setOutput(nodeID, res.Output)
	}
	if res.Err != nil {
		s.setError(nodeID, res.Err.Error())
		reason := store.FailureUnknown
		if n, gerr := s.backends.State.GetNode(s.ctx, s.runID, nodeID); gerr == nil && n != nil {
			reason = n.FailureReason
		}
		s.setFailure(nodeID, failureFrom(reason, res.Err))
	}
	s.setOutcome(nodeID, res.Outcome)
}

// runApprovalGate writes the approvals row and blocks until resolved.
// Approved -> Success; Denied -> Failed; timeout per ApprovalOnTimeout.
func (s *dispatchState) runApprovalGate(node *sparkwing.JobNode) runner.Result {
	cfg := node.ApprovalConfig()
	if cfg == nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("approval node %q has nil config", node.ID())}
	}

	nlog, err := s.backends.Logs.OpenNodeLog(s.runID, node.ID(), s.delegate)
	if err == nil {
		nlog = wrapNodeLogWithMasker(nlog, s.masker)
	}
	if err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	defer func() { _ = nlog.Close() }()

	if err := s.backends.State.StartNode(s.ctx, s.runID, node.ID()); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, node.ID(), "node_started", nil)
	nodeStartTS := time.Now()
	nlog.Emit(sparkwing.LogRecord{
		TS:    nodeStartTS,
		Level: "info",
		Event: "node_start",
	})

	timeoutMS := cfg.Timeout.Milliseconds()
	onTimeout := string(cfg.OnExpiry)
	if onTimeout == "" {
		onTimeout = store.ApprovalOnTimeoutFail
	}
	appr := store.Approval{
		RunID:       s.runID,
		NodeID:      node.ID(),
		RequestedAt: time.Now(),
		Message:     cfg.Message,
		TimeoutMS:   timeoutMS,
		OnTimeout:   onTimeout,
	}
	if err := s.backends.State.CreateApproval(s.ctx, appr); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("CreateApproval: %w", err)}
	}
	reqPayload, _ := json.Marshal(map[string]any{
		"message":    cfg.Message,
		"timeout_ms": timeoutMS,
	})
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, node.ID(), "approval_requested", reqPayload)
	nlog.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "approval_requested",
		Msg:   cfg.Message,
		Attrs: map[string]any{"timeout_ms": timeoutMS},
	})

	deadline := time.Time{}
	if cfg.Timeout > 0 {
		deadline = appr.RequestedAt.Add(cfg.Timeout)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	res := s.pollApproval(node.ID(), deadline, onTimeout, ticker)

	if res.via != "" {
		resAttrs := map[string]any{
			"resolution":  res.resolution,
			"via":         res.via,
			"duration_ms": time.Since(nodeStartTS).Milliseconds(),
		}
		if res.approver != "" {
			resAttrs["approver"] = res.approver
		}
		if res.comment != "" {
			resAttrs["comment"] = res.comment
		}
		nlog.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Event: "approval_resolved",
			Msg:   res.summary,
			Attrs: resAttrs,
		})
		_ = s.backends.State.AppendNodeAnnotation(s.ctx, s.runID, node.ID(), res.summary)
	}

	nlog.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "node_end",
		Attrs: map[string]any{
			"outcome":     string(res.outcome),
			"duration_ms": time.Since(nodeStartTS).Milliseconds(),
		},
	})

	if err := s.backends.State.FinishNode(s.ctx, s.runID, node.ID(), string(res.outcome), res.errMsg, res.payload); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	outcome, errMsg := res.outcome, res.errMsg
	if outcome == sparkwing.Failed && errMsg != "" {
		return runner.Result{Outcome: outcome, Err: errors.New(errMsg), Output: nil}
	}
	return runner.Result{Outcome: outcome}
}

// approvalResult bundles the outcome of a resolved approval gate
// plus the metadata operators want to see -- who approved (or "the
// timeout policy"), what they said, and how long the gate waited.
// The orchestrator surfaces these via the approval_resolved log
// event and a matching node annotation.
type approvalResult struct {
	outcome    sparkwing.Outcome
	errMsg     string
	payload    []byte
	resolution string
	approver   string
	comment    string
	via        string
	summary    string
}

// pollApproval blocks until a resolution appears or deadline fires.
// On deadline writes timed_out so a late human resolve becomes 409.
// The wedge guard bounds a wedged store: a "locking protocol" error
// or a GetApproval failure streak past the budget fails the gate
// instead of polling forever against a database another process has
// locked.
func (s *dispatchState) pollApproval(nodeID string, deadline time.Time, onTimeout string, ticker *time.Ticker) approvalResult {
	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		return approvalResult{outcome: sparkwing.Failed, errMsg: err.Error()}
	}
	for {
		got, err := s.backends.State.GetApproval(s.ctx, s.runID, nodeID)
		if err != nil {
			if terminal := wedge.fail(fmt.Sprintf("approval poll %s/%s", s.runID, nodeID), err); terminal != nil {
				return approvalResult{outcome: sparkwing.Failed, errMsg: terminal.Error()}
			}
		} else {
			wedge.success()
			if got.ResolvedAt != nil {
				return approvalResolutionToOutcome(got.Resolution, got.Approver, got.Comment)
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if _, err := s.backends.State.ResolveApproval(s.ctx, s.runID, nodeID,
				store.ApprovalResolutionTimedOut, "sparkwing", "timeout"); err != nil {
				if errors.Is(err, store.ErrLockHeld) {
					if got, err2 := s.backends.State.GetApproval(s.ctx, s.runID, nodeID); err2 == nil && got.ResolvedAt != nil {
						return approvalResolutionToOutcome(got.Resolution, got.Approver, got.Comment)
					}
				}
			}
			return approvalTimeoutToOutcome(onTimeout)
		}
		select {
		case <-ticker.C:
		case <-s.ctx.Done():
			return approvalResult{outcome: sparkwing.Cancelled, errMsg: "ctx-cancelled"}
		}
	}
}

// approvalResolutionToOutcome maps a stored resolution (human action
// or written-by-orchestrator timeout) to an approvalResult.
func approvalResolutionToOutcome(resolution, approver, comment string) approvalResult {
	payload, _ := json.Marshal(map[string]any{
		"resolution": resolution,
		"approver":   approver,
		"comment":    comment,
	})
	r := approvalResult{
		resolution: resolution,
		approver:   approver,
		comment:    comment,
		payload:    payload,
		via:        "human",
	}
	if approver == "sparkwing" {
		r.via = "timeout"
	}
	switch resolution {
	case store.ApprovalResolutionApproved:
		r.outcome = sparkwing.Success
		r.summary = fmt.Sprintf("approved by %s", approver)
		if comment != "" {
			r.summary += " · " + comment
		}
	case store.ApprovalResolutionDenied:
		r.outcome = sparkwing.Failed
		r.errMsg = fmt.Sprintf("denied by %s", approver)
		if comment != "" {
			r.errMsg += ": " + comment
		}
		r.summary = r.errMsg
	case store.ApprovalResolutionTimedOut:
		r.outcome = sparkwing.Failed
		r.errMsg = "approval timed out"
		r.summary = "approval timed out"
	default:
		r.outcome = sparkwing.Failed
		r.errMsg = "unknown approval resolution: " + resolution
		r.summary = r.errMsg
	}
	return r
}

// approvalTimeoutToOutcome applies the author-configured on_timeout
// when the deadline fires before any human acts. Surfaces the policy
// in the summary so an operator sees "auto-approved (timeout
// policy=approve)" rather than a bare Success.
func approvalTimeoutToOutcome(onTimeout string) approvalResult {
	r := approvalResult{via: "timeout-policy:" + onTimeout}
	switch onTimeout {
	case store.ApprovalOnTimeoutApprove:
		r.outcome = sparkwing.Success
		r.summary = "auto-approved (timeout policy=approve)"
	case store.ApprovalOnTimeoutDeny:
		r.outcome = sparkwing.Failed
		r.errMsg = "approval timed out (policy=deny)"
		r.summary = "auto-denied (timeout policy=deny)"
	default:
		r.outcome = sparkwing.Failed
		r.errMsg = "approval timed out (policy=fail)"
		r.summary = "approval timed out (policy=fail)"
	}
	return r
}

// invokeRecoveryRunner runs a recovery node via the in-process
// job-only path; cluster runners fall back to full RunNode.
func (s *dispatchState) invokeRecoveryRunner(node *sparkwing.JobNode, parentFailure sparkwing.Failure) runner.Result {
	ctx := sparkwing.WithFailure(s.resolverCtx, parentFailure)
	if ipr, ok := s.runner.(*InProcessRunner); ok {
		out, err := ipr.executeNode(ctx, s.runID, node, s.delegate)
		if err != nil {
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}
		return runner.Result{Outcome: sparkwing.Success, Output: out}
	}
	return s.runWithCap(node, func(slot *workerSlot) runner.Result {
		return s.runner.RunNode(ctx, runner.Request{
			RunID:               s.runID,
			NodeID:              node.ID(),
			Pipeline:            localPipelineFromContext(ctx),
			Node:                node,
			Delegate:            s.delegate,
			ReleaseWorkerSlot:   slot.release,
			ReacquireWorkerSlot: slot.reacquire,
		})
	})
}

// workerSlot is the MaxParallel reservation held while a node runs. It
// can be released and re-acquired so a node blocked on concurrency
// admission gives the slot back for the duration of the wait. The zero
// value (sem nil) is a no-op slot used when no cap is configured.
type workerSlot struct {
	sem  chan struct{}
	ctx  context.Context
	mu   sync.Mutex
	held bool
}

// release gives the worker slot back if currently held. Safe to call
// repeatedly.
func (w *workerSlot) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sem != nil && w.held {
		<-w.sem
		w.held = false
	}
}

// reacquire takes the worker slot again, blocking until one is free.
// Returns false if the run was cancelled first. A no-op slot (no cap)
// always reports true.
func (w *workerSlot) reacquire() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sem == nil || w.held {
		return true
	}
	select {
	case w.sem <- struct{}{}:
		w.held = true
		return true
	case <-w.ctx.Done():
		return false
	}
}

// runWithCap gates non-inline RunNode against the MaxParallel sem.
// Nil sem or inline node = no cap. The closure receives the held
// workerSlot so the concurrency-wait path can release it while blocked.
func (s *dispatchState) runWithCap(node *sparkwing.JobNode, fn func(slot *workerSlot) runner.Result) runner.Result {
	if s.sem == nil || node.IsInline() {
		return fn(&workerSlot{})
	}
	select {
	case s.sem <- struct{}{}:
	case <-s.resolverCtx.Done():
		return runner.Result{Outcome: sparkwing.Cancelled}
	}
	slot := &workerSlot{sem: s.sem, ctx: s.resolverCtx, held: true}
	defer slot.release()
	return fn(slot)
}

// safeCacheKey invokes the CacheKeyFn under the same budget rules as
// SkipIf predicates: panic recovery + timeout. Logs loudly into the
// node's ctx-logger on any failure and returns the empty string
// (which the caller treats as "no cache for this invocation").
func safeCacheKey(ctx context.Context, fn sparkwing.CacheKeyFn, nodeID string) sparkwing.CacheKey {
	pctx, cancel := context.WithTimeout(ctx, defaultPredicateTimeout)
	defer cancel()

	done := make(chan sparkwing.CacheKey, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sparkwing.LoggerFromContext(ctx).Log("error",
					fmt.Sprintf("CacheKey(%s) panicked: %v (proceeding uncached)", nodeID, r))
				done <- ""
			}
		}()
		done <- fn(pctx)
	}()

	select {
	case k := <-done:
		return k
	case <-pctx.Done():
		sparkwing.LoggerFromContext(ctx).Log("error",
			fmt.Sprintf("CacheKey(%s) exceeded %s budget (proceeding uncached)", nodeID, defaultPredicateTimeout))
		return ""
	}
}

// callBeforeRun runs a BeforeRun hook with panic recovery.
func callBeforeRun(ctx context.Context, hook sparkwing.BeforeRunFn) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return hook(ctx)
}

// callAfterRun runs an AfterRun hook with panic recovery; failure
// does not change the node's outcome.
func callAfterRun(ctx context.Context, hook sparkwing.AfterRunFn, runErr error, index int, nlog NodeLog) {
	defer func() {
		if r := recover(); r != nil {
			nlog.Log("error", fmt.Sprintf("AfterRun hook %d panicked: %v", index, r))
		}
	}()
	hook(ctx, runErr)
}

// scaledBackoff returns initial * 2^(attempt-1), capped at 5m.
func scaledBackoff(initial time.Duration, attempt int) time.Duration {
	if initial <= 0 || attempt <= 0 {
		return 0
	}
	out := initial
	for i := 1; i < attempt; i++ {
		out *= 2
		if out > 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return out
}

// markFailed finalizes a node whose dispatch machinery errored
// (Run was never invoked).
func (s *dispatchState) markFailed(nodeID string, reason error) {
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Failed), reason.Error(), nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_failed", []byte(reason.Error()))
	s.setOutcome(nodeID, sparkwing.Failed)
}

func (s *dispatchState) markCancelled(nodeID, reason string) {
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Cancelled), reason, nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_cancelled", []byte(reason))
	s.setOutcome(nodeID, sparkwing.Cancelled)
}

// markRunCancelled reclassifies a node the runner reported Failed whose
// failure was the run context being cancelled (a sibling failed and the
// run is tearing down), not a fault in the node's own work. Recording it
// Cancelled lets the summary attribute it to the cascade instead of
// listing it among the independent failures that a reader must sift
// through to find the real cause. The store write detaches from the
// cancelled run ctx so the terminal status still lands.
func (s *dispatchState) markRunCancelled(nodeID string) {
	ctx := context.WithoutCancel(s.ctx)
	_ = s.backends.State.FinishNode(ctx, s.runID, nodeID, string(sparkwing.Cancelled), "cancelled: run failing", nil)
	_ = s.backends.State.AppendEvent(ctx, s.runID, nodeID, "node_cancelled", []byte("cancelled: run failing"))
	s.setOutcome(nodeID, sparkwing.Cancelled)
}

// canceledByRun reports whether a Failed node's error is the run tearing
// the node down rather than a fault in the node's own work: the step
// observed a cancelled context, or exec.CommandContext SIGKILLed the
// child when the run context was cancelled. Only consulted once the run
// context is already cancelled, so a genuine SIGKILL on a healthy run is
// not misread as a cascade.
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

func (s *dispatchState) markSkipped(nodeID, reason string) {
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Skipped), reason, nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_skipped", []byte(reason))
	s.setOutcome(nodeID, sparkwing.Skipped)
}

// defaultPredicateTimeout caps a SkipIf predicate when the node
// doesn't override.
const defaultPredicateTimeout = 30 * time.Second

// evalSkipPredicates returns (reason, true) on the first true predicate.
// Errors/panics/timeouts don't skip -- run the work and let the job decide.
func evalSkipPredicates(ctx context.Context, node *sparkwing.JobNode) (string, bool) {
	preds := node.SkipPredicates()
	if len(preds) == 0 {
		return "", false
	}
	budget := node.SkipIfBudget()
	if budget == 0 {
		budget = defaultPredicateTimeout
	}
	logger := sparkwing.LoggerFromContext(ctx)
	for i, pred := range preds {
		if len(preds) > 1 {
			logger.Log("info",
				fmt.Sprintf("evaluating SkipIf predicate %d/%d (budget %s)", i+1, len(preds), budget))
		} else {
			logger.Log("info", fmt.Sprintf("evaluating SkipIf predicate (budget %s)", budget))
		}
		if skipped, reason := runOnePredicate(ctx, pred, i, budget); skipped {
			return reason, true
		}
	}
	return "", false
}

// runOnePredicate evaluates pred under timeout + panic recovery;
// non-success defaults to "don't skip".
func runOnePredicate(ctx context.Context, pred sparkwing.SkipPredicate, index int, budget time.Duration) (bool, string) {
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				sparkwing.LoggerFromContext(ctx).Log("error",
					fmt.Sprintf("SkipIf predicate %d panicked: %v (defaulting to don't-skip)", index, r))
				done <- false
			}
		}()
		done <- pred(pctx)
	}()

	select {
	case result := <-done:
		if result {
			return true, fmt.Sprintf("SkipIf predicate %d returned true", index)
		}
		return false, ""
	case <-pctx.Done():
		sparkwing.LoggerFromContext(ctx).Log("error",
			fmt.Sprintf("SkipIf predicate %d exceeded %s budget (defaulting to don't-skip); raise via SkipIf(fn, sparkwing.SkipBudget(d))", index, budget))
		return false, ""
	}
}

// planSnapshot is the stored DAG with each Node's eager-materialized
// inner Work tree.
type planSnapshot struct {
	Pipeline  string         `json:"pipeline"`
	RunID     string         `json:"run_id"`
	Nodes     []snapshotNode `json:"nodes"`
	PlanConc  *snapshotConc  `json:"plan_concurrency,omitempty"`
	PlanConcs []snapshotConc `json:"plan_concurrency_groups,omitempty"`

	// Resources is the plan-level cold-start cost hint set declared via
	// Plan.Resources; nil when the pipeline declared none.
	Resources *snapshotResources `json:"plan_resources,omitempty"`

	// Secrets is the typed declaration the pipelines.yaml file
	// shipped (name + required/optional). The cluster pod uses it
	// to drive ResolvePipelineSecrets against the pod's existing
	// SecretResolver. Values are never persisted -- secrets are
	// re-resolved on the pod side, never shipped across the wire.
	Secrets pipelines.SecretsField `json:"secrets,omitempty"`
}

type snapshotNode struct {
	ID   string            `json:"id"`
	Deps []string          `json:"deps"`
	Env  map[string]string `json:"env,omitempty"`
	// Named NodeGroups this node belongs to.
	Groups   []string          `json:"groups,omitempty"`
	Dynamic  bool              `json:"dynamic,omitempty"`
	Approval *snapshotApproval `json:"approval,omitempty"`
	// OnFailureOf is the parent whose .OnFailure attached this node.
	OnFailureOf string `json:"on_failure_of,omitempty"`

	Modifiers *snapshotModifiers `json:"modifiers,omitempty"`

	// Work is the eager-materialized inner DAG; nil for gates and
	// recovery nodes without a Job.
	Work *snapshotWork `json:"work,omitempty"`
}

type snapshotConc struct {
	Key string `json:"key,omitempty"`
}

// snapshotResources is the wire shape of a ResourceHints declaration:
// advisory peak-usage estimates for admission, never limits.
type snapshotResources struct {
	Cores       float64 `json:"cores,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
}

// snapshotApproval is the wire shape of an approval gate's prompt +
// timeout policy.
type snapshotApproval struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

// snapshotModifiers is the wire shape of a Node's Plan-layer modifiers.
type snapshotModifiers struct {
	Retry          int      `json:"retry,omitempty"`
	RetryBackoffMS int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto      bool     `json:"retry_auto,omitempty"`
	TimeoutMS      int64    `json:"timeout_ms,omitempty"`
	RunsOn         []string `json:"runs_on,omitempty"`
	Prefers        []string `json:"prefers,omitempty"`
	WhenRunner     []string `json:"when_runner,omitempty"`
	// Content cache (JobNode.Cache): independent of any concurrency
	// group. Cache marks that the node memoizes on content; CacheTTLMS
	// is the retention window.
	Cache      bool  `json:"cache,omitempty"`
	CacheTTLMS int64 `json:"cache_ttl_ms,omitempty"`
	// Concurrency group membership (JobNode.Concurrency): name, the
	// declared budget + this member's cost, scope, the at-limit policy,
	// and the optional timeouts. All independent of the content cache.
	ConcGroup           string `json:"conc_group,omitempty"`
	ConcCapacity        int    `json:"conc_capacity,omitempty"`
	ConcCost            int    `json:"conc_cost,omitempty"`
	ConcScope           string `json:"conc_scope,omitempty"`
	ConcOnLimit         string `json:"conc_on_limit,omitempty"`
	ConcQueueTimeoutMS  int64  `json:"conc_queue_timeout_ms,omitempty"`
	ConcCancelTimeoutMS int64  `json:"conc_cancel_timeout_ms,omitempty"`
	// Resource hints (JobNode.Resources): advisory peak-usage
	// estimates for admission. Independent of concurrency groups.
	ResCores        float64 `json:"res_cores,omitempty"`
	ResMemoryBytes  int64   `json:"res_memory_bytes,omitempty"`
	Inline          bool    `json:"inline,omitempty"`
	Optional        bool    `json:"optional,omitempty"`
	ContinueOnError bool    `json:"continue_on_error,omitempty"`
	OnFailure       string  `json:"on_failure,omitempty"`
	HasBeforeRun    bool    `json:"has_before_run,omitempty"`
	HasAfterRun     bool    `json:"has_after_run,omitempty"`
	HasSkipIf       bool    `json:"has_skip_if,omitempty"`
}

// snapshotWork is the wire shape of a Job's inner DAG.
type snapshotWork struct {
	Steps      []snapshotStep      `json:"steps,omitempty"`
	Spawns     []snapshotSpawn     `json:"spawns,omitempty"`
	SpawnEach  []snapshotSpawnEach `json:"spawn_each,omitempty"`
	StepGroups []snapshotStepGroup `json:"step_groups,omitempty"`
	ResultStep string              `json:"result_step,omitempty"`
}

// snapshotStepGroup is the wire shape of a sparkwing.GroupSteps
// declaration: a named bundle of step IDs the dashboard renders as
// a collapsible cluster inside the inner Work DAG. Groups are
// emitted in author declaration order; member IDs preserve the order
// they were passed to GroupSteps. Anonymous groups (empty Name) are
// still emitted so structural-only groupings round-trip.
type snapshotStepGroup struct {
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members"`
}

type snapshotStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
	// Risks is the author-declared risk-label set on this step.
	// Empty when no label was declared. Surfaced in the plan
	// snapshot so `pipeline explain --json` consumers see the
	// contract alongside the static DAG.
	Risks []string `json:"risks,omitempty"`
}

type snapshotSpawn struct {
	ID         string        `json:"id"`
	Needs      []string      `json:"needs,omitempty"`
	TargetJob  string        `json:"target_job,omitempty"`
	TargetWork *snapshotWork `json:"target_work,omitempty"`
	HasSkipIf  bool          `json:"has_skip_if,omitempty"`
}

type snapshotSpawnEach struct {
	ID               string        `json:"id"`
	Needs            []string      `json:"needs,omitempty"`
	TargetJob        string        `json:"target_job,omitempty"`
	ItemTemplateWork *snapshotWork `json:"item_template_work,omitempty"`
	Note             string        `json:"note,omitempty"`
}

// planSnapshotMeta carries the run-level fields the cluster pod
// needs to re-resolve PipelineSecrets when it picks up a node.
// Zero-value omits the fields from the emitted JSON.
type planSnapshotMeta struct {
	Secrets          pipelines.SecretsField
	PipelineRequires []string
}

func marshalPlanSnapshot(p *sparkwing.Plan, rc sparkwing.RunContext, meta planSnapshotMeta) ([]byte, error) {
	snap := planSnapshot{
		Pipeline: rc.Pipeline,
		RunID:    rc.RunID,
		Secrets:  meta.Secrets,
	}
	if group := p.ConcurrencyGroupRef(); group != nil {
		snap.PlanConc = &snapshotConc{
			Key: scopedGroupKey(group, rc.RunID),
		}
	}
	for _, membership := range p.PlanConcurrency() {
		if membership.Group == nil {
			continue
		}
		snap.PlanConcs = append(snap.PlanConcs, snapshotConc{
			Key: scopedGroupKey(membership.Group, rc.RunID),
		})
	}
	if rh := p.ResourceHints(); rh != nil {
		snap.Resources = &snapshotResources{
			Cores:       rh.Cores,
			MemoryBytes: rh.MemoryBytes,
		}
	}
	walker := newWorkWalker()
	seen := make(map[string]bool)
	for _, n := range p.Nodes() {
		sn := snapshotNode{
			ID:      n.ID(),
			Deps:    n.DepIDs(),
			Env:     n.EnvMap(),
			Groups:  p.JobGroupNames(n.ID()),
			Dynamic: p.IsDynamicNode(n.ID()),
		}
		if cfg := n.ApprovalConfig(); cfg != nil {
			sn.Approval = &snapshotApproval{
				Message:   cfg.Message,
				TimeoutMS: cfg.Timeout.Milliseconds(),
				OnTimeout: string(cfg.OnExpiry),
			}
		}
		sn.Modifiers = nodeModifiersSnapshot(n)
		if w := n.Work(); w != nil {
			work, err := walker.walk(w, n.ResultStep())
			if err != nil {
				return nil, fmt.Errorf("plan node %q: %w", n.ID(), err)
			}
			sn.Work = work
		}
		snap.Nodes = append(snap.Nodes, sn)
		seen[n.ID()] = true
	}
	for _, n := range p.Nodes() {
		rec := n.OnFailureNode()
		if rec == nil || seen[rec.ID()] {
			continue
		}
		recSnap := snapshotNode{
			ID:          rec.ID(),
			Deps:        rec.DepIDs(),
			Env:         rec.EnvMap(),
			Groups:      p.JobGroupNames(rec.ID()),
			OnFailureOf: n.ID(),
			Modifiers:   nodeModifiersSnapshot(rec),
		}
		if w := rec.Work(); w != nil {
			work, err := walker.walk(w, rec.ResultStep())
			if err != nil {
				return nil, fmt.Errorf("plan node %q (on_failure of %q): %w", rec.ID(), n.ID(), err)
			}
			recSnap.Work = work
		}
		snap.Nodes = append(snap.Nodes, recSnap)
		seen[rec.ID()] = true
	}
	return json.Marshal(snap)
}

// effectiveClaimLabels is the combined Requires + WhenRunner label
// list persisted on the store row. Pool runners polling
// ClaimNextReadyNode filter against this set, so a WhenRunner term is
// enforced for any runner whose advertisement is consulted by the
// claim path. The orchestrator separately gates WhenRunner up-front
// (see runOneNode) against the active runner via LabelAdvertiser, so
// the in-process path doesn't depend on the claim-time filter.
func effectiveClaimLabels(n *sparkwing.JobNode, pipelineRequires []string) []string {
	req := effectiveJobRequires(n, pipelineRequires)
	when := n.WhenRunnerLabels()
	if len(when) == 0 {
		return req
	}
	out := make([]string, 0, len(req)+len(when))
	out = append(out, req...)
	seen := make(map[string]struct{}, len(req))
	for _, l := range req {
		seen[l] = struct{}{}
	}
	for _, l := range when {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// effectiveJobRequires unions the node's own RequiresLabels with the
// pipeline-level requires list (from pipeline.requires or
// defaults.requires, whichever resolved). Dedupes; preserves the
// node's label order first, then appends new ones from the pipeline.
// The reserved "local" label is dropped here -- it pins the run to
// in-process via opts.LocalOnly elsewhere, so passing it through to
// runner claim filtering would over-constrain.
func effectiveJobRequires(n *sparkwing.JobNode, pipelineRequires []string) []string {
	own := n.RequiresLabels()
	if len(pipelineRequires) == 0 {
		return own
	}
	out := make([]string, 0, len(own)+len(pipelineRequires))
	seen := make(map[string]struct{}, len(own)+len(pipelineRequires))
	for _, l := range own {
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	for _, l := range pipelineRequires {
		if l == "local" {
			continue
		}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}

// nodeModifiersSnapshot extracts the Plan-layer modifiers a renderer
// cares about. Returns nil when nothing is set so JSON omits the
// field entirely.
func nodeModifiersSnapshot(n *sparkwing.JobNode) *snapshotModifiers {
	rc := n.RetryConfig()
	m := snapshotModifiers{
		Retry:           rc.Attempts,
		RetryBackoffMS:  rc.Backoff.Milliseconds(),
		RetryAuto:       rc.Auto,
		TimeoutMS:       n.TimeoutDuration().Milliseconds(),
		RunsOn:          n.RequiresLabels(),
		Prefers:         n.PrefersLabels(),
		WhenRunner:      n.WhenRunnerLabels(),
		Inline:          n.IsInline(),
		Optional:        n.IsOptional(),
		ContinueOnError: n.IsContinueOnError(),
		HasBeforeRun:    len(n.BeforeRunHooks()) > 0,
		HasAfterRun:     len(n.AfterRunHooks()) > 0,
		HasSkipIf:       len(n.SkipPredicates()) > 0,
	}
	if rec := n.OnFailureNode(); rec != nil {
		m.OnFailure = rec.ID()
	}
	if cc := n.CacheConfig(); cc != nil {
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

// isZeroModifiers reports whether m has no fields set.
func isZeroModifiers(m snapshotModifiers) bool {
	return m.Retry == 0 &&
		m.RetryBackoffMS == 0 &&
		!m.RetryAuto &&
		m.TimeoutMS == 0 &&
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
		!m.HasSkipIf
}

// workWalker recurses Spawn targets, detecting cycles and memoizing
// per Job reflect-type so identical Jobs share one snapshot.
type workWalker struct {
	stack    []string
	stackSet map[string]bool
	memo     map[string]*snapshotWork
}

func newWorkWalker() *workWalker {
	return &workWalker{
		stackSet: map[string]bool{},
		memo:     map[string]*snapshotWork{},
	}
}

func (w *workWalker) walk(work *sparkwing.Work, resultStep *sparkwing.WorkStep) (*snapshotWork, error) {
	out := &snapshotWork{}
	if resultStep != nil {
		out.ResultStep = resultStep.ID()
	}
	for _, s := range work.Steps() {
		out.Steps = append(out.Steps, snapshotStep{
			ID:        s.ID(),
			Needs:     s.DepIDs(),
			IsResult:  s == resultStep,
			HasSkipIf: len(s.SkipPredicates()) > 0,
			Risks:     s.Risks(),
		})
	}
	for _, s := range work.Spawns() {
		spawn := snapshotSpawn{
			ID:        s.ID(),
			Needs:     s.DepIDs(),
			TargetJob: jobName(s.Job()),
			HasSkipIf: len(s.SkipPredicates()) > 0,
		}
		target, err := w.walkJob(s.Job())
		if err != nil {
			return nil, err
		}
		spawn.TargetWork = target
		out.Spawns = append(out.Spawns, spawn)
	}
	for _, g := range work.Groups() {
		members := g.Members()
		ids := make([]string, len(members))
		for i, m := range members {
			ids[i] = m.ID()
		}
		out.StepGroups = append(out.StepGroups, snapshotStepGroup{
			Name:    g.Name(),
			Members: ids,
		})
	}
	for _, g := range work.SpawnGens() {
		each := snapshotSpawnEach{
			ID:    g.ID(),
			Needs: g.DepIDs(),
		}
		if id, job, err := materializeSpawnEachTemplate(g); err == nil && job != nil {
			each.TargetJob = jobName(job)
			tmpl, werr := w.walkJob(job)
			if werr != nil {
				return nil, werr
			}
			each.ItemTemplateWork = tmpl
			if id != "" {
				each.Note = fmt.Sprintf("template materialized from zero-value input; sample id=%q", id)
			} else {
				each.Note = "template materialized from zero-value input"
			}
		} else if err != nil {
			each.Note = fmt.Sprintf("template not materializable: %s", err.Error())
		}
		out.SpawnEach = append(out.SpawnEach, each)
	}
	return out, nil
}

func (w *workWalker) walkJob(job sparkwing.Workable) (*snapshotWork, error) {
	if job == nil {
		return nil, nil
	}
	key := jobName(job)
	if w.stackSet[key] {
		cycle := append([]string{}, w.stack...)
		cycle = append(cycle, key)
		return nil, fmt.Errorf("spawn cycle detected: %s", joinCycle(cycle))
	}
	if cached, ok := w.memo[key]; ok {
		return cached, nil
	}
	w.stack = append(w.stack, key)
	w.stackSet[key] = true
	defer func() {
		w.stack = w.stack[:len(w.stack)-1]
		delete(w.stackSet, key)
	}()
	work := sparkwing.NewWork()
	resultStep, err := job.Work(work)
	if err != nil {
		return nil, fmt.Errorf("Job.Work failed: %w", err)
	}
	out, err := w.walk(work, resultStep)
	if err != nil {
		return nil, err
	}
	w.memo[key] = out
	return out, nil
}

// jobName returns a stable per-type identifier (reflect type string).
func jobName(job sparkwing.Workable) string {
	if job == nil {
		return "<nil>"
	}
	t := reflect.TypeOf(job)
	if t == nil {
		return "<unknown>"
	}
	return t.String()
}

func joinCycle(parts []string) string {
	return strings.Join(parts, " -> ")
}

// materializeSpawnEachTemplate invokes a SpawnGen with a zero-value
// input under panic recovery to render a template.
func materializeSpawnEachTemplate(spec *sparkwing.SpawnGenSpec) (id string, job sparkwing.Workable, err error) {
	defer func() {
		if r := recover(); r != nil {
			id = ""
			job = nil
			err = fmt.Errorf("generator panicked on zero-value input: %v", r)
		}
	}()
	fn := reflect.ValueOf(spec.Fn())
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return "", nil, errors.New("generator fn is not a function")
	}
	t := fn.Type()
	if t.NumIn() != 1 {
		return "", nil, fmt.Errorf("generator fn takes %d args (want 1)", t.NumIn())
	}
	if t.NumOut() != 2 {
		return "", nil, fmt.Errorf("generator fn returns %d values (want 2)", t.NumOut())
	}
	zero := reflect.Zero(t.In(0))
	out := fn.Call([]reflect.Value{zero})
	if !out[0].IsValid() || out[0].Kind() != reflect.String {
		return "", nil, errors.New("generator fn first return is not string id")
	}
	id = out[0].String()
	if out[1].IsValid() {
		raw := out[1].Interface()
		if raw != nil {
			j, cerr := sparkwing.CoerceSpawnEachJob(raw)
			if cerr != nil {
				return id, nil, cerr
			}
			job = j
		}
	}
	return id, job, nil
}

// newRunID returns a sortable id like run-20260420-093112-a7f2.
func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}
