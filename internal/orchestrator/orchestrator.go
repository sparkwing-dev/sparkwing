package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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

	LocalOnly bool

	DryRun bool

	Debug DebugDirectives

	SecretSource secrets.Source

	PipelineYAML *pipelines.Pipeline

	MaxParallel int

	DispatchWaitTimeout time.Duration

	LogStore storage.LogStore

	ArtifactStore storage.ArtifactStore

	State storage.StateStore

	DefaultStateDB string

	ProfileLookup storeurl.ProfileLookup

	Profile *profile.Profile

	ProfileChain *profile.Chain

	DefaultArgs map[string]string

	MirrorLocal *store.Store
}

type DebugDirectives struct {
	PauseBefore []string

	PauseAfter []string

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

type Result struct {
	RunID  string
	Status string
	Error  error
}

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

	invokeArgs := mergeInvokeArgs(opts)

	if opts.PipelineYAML != nil {

		guardCtx := pipelines.GuardContext{
			Args: invokeArgs,
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
	invocation := buildRunInvocation(opts, runID, localRunLogDir(backends.Logs, runID), reg.SecretArgNames())
	if opts.RunHandlePath != "" {
		release, err := reserveRunHandle(opts.RunHandlePath)
		if err != nil {
			return nil, fmt.Errorf("reserve run handle: %w", err)
		}
		defer release()
	}
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
	if opts.RunHandlePath != "" {
		handle := NewRunHandle(runID, opts.Pipeline, localRunLogDir(backends.Logs, runID), "running")
		if err := publishRunHandle(opts.RunHandlePath, handle); err != nil {
			msg := fmt.Sprintf("publish run handle: %v", err)
			_ = backends.State.FinishRun(context.WithoutCancel(ctx), runID, "failed", msg)
			return nil, errors.New(msg)
		}
	}

	hbCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go runRunHeartbeatLoop(hbCtx, 30*time.Second, backends.State, runID, wedgeBudget)

	masker := maskerForInvokeArgs(reg, invokeArgs)

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
	if opts.RetryPlanHash != "" {
		comparisonRC := rc
		comparisonRC.RunID = opts.RetryOf
		comparison, cerr := marshalPlanSnapshot(plan, comparisonRC, snapMeta)
		actual := hashBytes(comparison)
		if cerr != nil || actual != opts.RetryPlanHash {
			if cerr != nil {
				err = fmt.Errorf("retry provenance drift: compare plan snapshot: %w", cerr)
			} else {
				err = fmt.Errorf("retry provenance drift: source plan %s, checkout plan %s", opts.RetryPlanHash, actual)
			}
			_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
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

	if err := validatePlanModifiers(opts.Delegate, plan); err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}

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
		r = NewNodeExecutor(backends)
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

	if backends.LocalCoordination {
		profileName := ""
		if opts.Profile != nil {
			profileName = opts.Profile.Name
		}
		consumerCtx, cancelConsumer := context.WithCancel(ctx)
		defer cancelConsumer()
		go runLocalTriggerLoop(consumerCtx, backends.State, runID, profileName, parentTriggerRepoDir(), nil, wedgeBudget)
	}

	dispatchWaitTimeout := opts.DispatchWaitTimeout
	if dispatchWaitTimeout == 0 {
		dispatchWaitTimeout = defaultDispatchWaitTimeoutForPlan(plan)
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	var lease *runLease
	var leaseToken string
	var leaseChildToken string
	var leaseHostAdmitted bool
	skipDispatch := false
	if opts.Admission != nil {
		if opts.Admission.Delegate == nil {
			opts.Admission.Delegate = opts.Delegate
		}
		var outcome admitOutcome
		var admitErr error
		lease, outcome, admitErr = opts.Admission.admitRun(runCtx, backends, opts.Pipeline, runID, plan, opts.MaxParallel, cancelRun)
		if admitErr != nil {

			degrade, refusal := opts.Admission.unhostedOutcome(admitErr, plan, opts.DryRun)
			switch {
			case refusal != nil:
				admitErr = refusal
			case degrade:

				opts.Admission = nil
				lease, outcome, admitErr = nil, admitProceed, nil
			}
		}
		if admitErr != nil {
			if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) {
				admitErr = cause
			}
			status := statusForRunError(admitErr)
			_ = backends.State.FinishRun(context.WithoutCancel(ctx), runID, status, admitErr.Error())
			if opts.Delegate != nil {
				attrs := map[string]any{
					"run_id": runID,
					"status": status,
					"error":  admitErr.Error(),
				}
				if runID != "" {
					attrs["hints"] = map[string]string{
						"status": "sparkwing runs status --run " + runID,
						"logs":   "sparkwing runs logs --run " + runID,
					}
				}
				opts.Delegate.Emit(sparkwing.LogRecord{
					TS:    time.Now(),
					Level: "error",
					Event: "run_finish",
					Attrs: attrs,
				})
			}
			return &Result{RunID: runID, Status: status, Error: admitErr}, nil
		}
		// safety: release only after FinishRun below, so the daemon's
		// orphan finalizer can never observe a still-running row.
		defer lease.release()
		if outcome == admitSkipped {
			skipDispatch = true
		} else if lease != nil {
			// safety: a run that degraded to unadmitted holds no lease and
			// still proceeds, so this dereference is guarded rather than
			// implied by admitErr == nil.
			leaseToken = lease.token
			leaseChildToken = lease.childToken
			leaseHostAdmitted = lease.hostAdmitted
		}
	}

	execStart := time.Now()
	var runErr error
	if !skipDispatch {
		runErr = dispatch(
			runCtx, backends, r, runID, plan, delegate, opts.Debug, opts.RetryOf,
			opts.Pipeline, opts.Full, masker, opts.MaxParallel, snapMeta, onlySkip,
			dispatchWaitTimeout, opts.Admission, leaseToken, leaseChildToken, leaseHostAdmitted,
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
		if backends.LocalCoordination {
			charge := runCharge{}
			if lease != nil {
				charge = lease.charge
			}
			recordRunProfile(finishCtx, backends.State, currentProfileKey(opts.Pipeline), runID, planPin(plan), capacityFingerprint(plan), charge, contended, execStart, time.Now())
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
		if backends.LocalCoordination && opts.Pipeline != "" {
			_ = backends.State.RecordContention(finishCtx, currentProfileKey(opts.Pipeline))
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

type nodeSupersededError struct {
	nodes []string
}

func (e *nodeSupersededError) Error() string {
	return fmt.Sprintf("nodes superseded by a newer arrival: %v", e.nodes)
}

func nodeFailureError(failed, cancelled []string) error {
	if len(failed) > 0 {
		if len(cancelled) > 0 {
			return fmt.Errorf("nodes failed: %v; %d more cancelled with the run", failed, len(cancelled))
		}
		return fmt.Errorf("nodes failed: %v", failed)
	}
	return fmt.Errorf("nodes cancelled: %d node(s) stopped before completing", len(cancelled))
}

type runDaemonCanceledError struct {
	reason string
}

func (e *runDaemonCanceledError) Error() string { return e.reason }

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

func RunLocal(ctx context.Context, paths Paths, opts Options) (*Result, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, fmt.Errorf("ensure sparkwing root: %w", err)
	}
	if opts.LocalOnly || opts.SecretSource == nil {
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

	if opts.Runner == nil && opts.ProcessPerNode {
		exec, eerr := setupLocalExecution(paths, &opts, backends, nodeWorkspace(), nil)
		if eerr != nil {
			return nil, eerr
		}
		if exec != nil {
			defer exec.cleanup()
			exec.runner.SetLeaseTokenSource(leaseTokensFromContext)
			opts.Runner = NewNodeExecutor(backends).WithSpawner(exec.runner)
		}
	}

	res, runErr := Run(ctx, backends, opts)
	if st != nil && opts.ArtifactStore != nil && res != nil && res.RunID != "" {
		if err := DumpRunState(ctx, st, res.RunID, opts.ArtifactStore); err != nil {
			fmt.Fprintf(os.Stderr, "warn: state dump failed: %v\n", err)
		}
	}
	return res, runErr
}

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

func dispatch(
	ctx context.Context,
	backends Backends,
	r runner.Runner,
	runID string,
	plan *sparkwing.Plan,
	delegate sparkwing.Logger,
	debug DebugDirectives,
	retryOf string,
	pipeline string,
	full bool,
	masker *secrets.Masker,
	maxParallel int,
	snapMeta planSnapshotMeta,
	onlySkip map[string]string,
	dispatchWaitTimeout time.Duration,
	admission *LocalAdmission,
	leaseToken string,
	leaseChildToken string,
	leaseHostAdmitted bool,
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
		dispatchCtx, backends, r, runID, pipeline, plan, delegate, debug, retryOf,
		masker, maxParallel, admission, leaseToken, leaseChildToken, leaseHostAdmitted,
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

	if waitForDispatch(&state.wg, dispatchWaitTimeout, state.admissionWaits, state.watchdogActiveNodeIDs) == dispatchWaitTimedOut {
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
	var cancelled []string
	var superseded []string
	for _, n := range plan.Nodes() {
		oc, ok := state.getOutcome(n.ID())
		if !ok || oc.OK() {
			continue
		}
		if n.IsOptional() {
			continue
		}
		switch oc {
		case sparkwing.Superseded:
			superseded = append(superseded, n.ID())
		case sparkwing.Cancelled:
			cancelled = append(cancelled, n.ID())
		default:
			failed = append(failed, n.ID())
		}
	}

	emitRunSummary(delegate, plan, state, runStart, len(failed) == 0 && len(cancelled) == 0 && len(superseded) == 0)

	if len(failed) > 0 || len(cancelled) > 0 {
		planReleaseOutcome = "failed"
		return nodeFailureError(failed, cancelled)
	}
	if len(superseded) > 0 {
		planReleaseOutcome = "superseded"
		return &nodeSupersededError{nodes: superseded}
	}
	return nil
}

func validatePlanModifiers(delegate sparkwing.Logger, plan *sparkwing.Plan) error {
	if err := planOutputTypeErrors(plan); err != nil {
		return err
	}
	if delegate == nil {
		return nil
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
	return nil
}

func parentTriggerRepoDir() string {
	if wd := sparkwing.WorkDir(); wd != "" {
		return wd
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

func maskerForInvokeArgs(reg *sparkwing.Registration, invokeArgs map[string]string) *secrets.Masker {
	masker := secrets.NewMasker()
	for _, v := range reg.SecretValues(invokeArgs) {
		masker.Register(v)
	}
	return masker
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

func checkoutProjectConfig(logger *slog.Logger) *projectconfig.Config {
	root := sparkwing.CurrentRuntime().WorkDir
	if root == "" {
		return nil
	}
	path := filepath.Join(root, ".sparkwing", projectconfig.Filename)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("project config unreadable; running with the caller's arguments only, "+
			"which may differ from the arguments this run was dispatched with",
			"path", path, "err", err)
		return nil
	}
	return cfg
}

func checkoutInvokeArgs(pipeline string, stored map[string]string, logger *slog.Logger) map[string]string {
	cfg := checkoutProjectConfig(logger)
	if cfg == nil {
		return stored
	}
	return mergeInvokeArgs(Options{
		Args:         stored,
		DefaultArgs:  cfg.Defaults.Args,
		PipelineYAML: (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline),
	})
}

func applyCheckoutProjectConfig(opts *Options, logger *slog.Logger) {
	if opts.DefaultArgs != nil && opts.PipelineYAML != nil {
		return
	}
	cfg := checkoutProjectConfig(logger)
	if cfg == nil {
		return
	}
	if opts.DefaultArgs == nil {
		opts.DefaultArgs = cfg.Defaults.Args
	}
	if opts.PipelineYAML == nil {
		entry := (&pipelines.Config{Pipelines: cfg.Pipelines}).Find(opts.Pipeline)
		if entry != nil && entry.Guards.IsEmpty() {
			entry.Guards = cfg.Defaults.Guards
		}
		opts.PipelineYAML = entry
	}
}

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
		if !containsNamedArg(opts.Args, secretArgs) {
			inv["inputs_hash"] = hashCanonicalJSON(opts.Args)
		}
	}

	if len(secretArgs) > 0 {
		inv[store.InvocationSecretArgsKey] = secretArgs
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
	if opts.LocalOnly {
		inv["backends"] = map[string]any{
			"state": "sqlite",
			"logs":  "filesystem",
			"cache": "filesystem",
		}
	} else if opts.Profile != nil && opts.ProfileChain != nil {
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

func containsNamedArg(args map[string]string, names []string) bool {
	for _, name := range names {
		if _, ok := args[name]; ok {
			return true
		}
	}
	return false
}

func emitRunStart(delegate sparkwing.Logger, invocation map[string]any) {
	if delegate == nil {
		return
	}
	delegate.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "run_start",
		Attrs: store.RedactInvocation(invocation),
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
	if opts.DryRun {
		flags["dry_run"] = true
	}
	if opts.LocalOnly {
		flags["local_only"] = true
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
	if !opts.LocalOnly {
		if v := os.Getenv("SPARKWING_PROFILE"); v != "" {
			flags["profile"] = v
		}
		if v := os.Getenv("SPARKWING_SECRETS_PROFILE"); v != "" {
			flags["secrets"] = v
		}
	}
	if v := os.Getenv("SPARKWING_MODE"); v != "" {
		flags["mode"] = v
	}
	if os.Getenv("SPARKWING_LOG_LEVEL") == "debug" {
		flags["verbose"] = true
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
		if k == "local_only" {
			flagName = "--sw-local-only"
		}
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

type planEdge struct {
	ID   string   `json:"id"`
	Deps []string `json:"deps"`
}

func planEdges(nodes []*sparkwing.JobNode) []planEdge {
	edges := make([]planEdge, 0, len(nodes))
	for _, n := range nodes {
		deps := append([]string(nil), n.DepIDs()...)
		sort.Strings(deps)
		edges = append(edges, planEdge{ID: n.ID(), Deps: deps})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}

func planTopologyHash(nodes []*sparkwing.JobNode) string {
	return hashCanonicalJSON(planEdges(nodes))
}

func capacityFingerprint(plan *sparkwing.Plan) string {
	type membership struct {
		Node     string `json:"node,omitempty"`
		Group    string `json:"group"`
		Capacity int    `json:"capacity"`
		Scope    string `json:"scope"`
		Policy   string `json:"policy"`
		Cost     int    `json:"cost"`
	}
	doc := struct {
		Edges       []planEdge   `json:"edges"`
		Concurrency []membership `json:"concurrency,omitempty"`
	}{Edges: planEdges(plan.Nodes())}
	appendGroup := func(node string, g *sparkwing.ConcurrencyGroup, cost int) {
		limit := g.Limit()
		scope := limit.Scope
		if scope == "" {
			scope = sparkwing.ScopeGlobal
		}
		policy := limit.OnLimit
		if policy == "" {
			policy = sparkwing.Queue
		}
		doc.Concurrency = append(doc.Concurrency, membership{
			Node:     node,
			Group:    g.Name(),
			Capacity: limit.Capacity,
			Scope:    string(scope),
			Policy:   string(policy),
			Cost:     cost,
		})
	}
	for _, pc := range plan.PlanConcurrency() {
		if pc.Group != nil {
			appendGroup("", pc.Group, pc.Cost)
		}
	}
	for _, n := range plan.Nodes() {
		if g := n.ConcurrencyGroupRef(); g != nil {
			appendGroup(n.ID(), g, n.ConcurrencyCost())
		}
	}
	sort.Slice(doc.Concurrency, func(i, j int) bool {
		a, b := doc.Concurrency[i], doc.Concurrency[j]
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.Group < b.Group
	})
	return hashCanonicalJSON(doc)
}

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

type dispatchState struct {
	ctx              context.Context
	resolverCtx      context.Context
	backends         Backends
	runner           runner.Runner
	runID            string
	pipeline         string
	plan             *sparkwing.Plan
	delegate         sparkwing.Logger
	pipelineRequires []string

	mu        sync.Mutex
	doneCh    map[string]chan struct{}
	outputsJS map[string][]byte
	outcomes  map[string]sparkwing.Outcome
	errors    map[string]string
	failures  map[string]sparkwing.Failure
	starts    map[string]time.Time
	durations map[string]time.Duration
	claimedBy map[string]string
	scheduled map[string]*sparkwing.JobNode

	inlineRunner runner.Runner
	debug        DebugDirectives

	retryOf string

	masker *secrets.Masker

	sem chan struct{}

	snapMeta planSnapshotMeta

	onlySkip map[string]string

	wg             sync.WaitGroup
	admissionWaits *admissionWaitTracker
}

func newDispatchState(
	ctx context.Context,
	backends Backends,
	r runner.Runner,
	runID string,
	pipeline string,
	plan *sparkwing.Plan,
	delegate sparkwing.Logger,
	debug DebugDirectives,
	retryOf string,
	masker *secrets.Masker,
	maxParallel int,
	admission *LocalAdmission,
	leaseToken string,
	leaseChildToken string,
	leaseHostAdmitted bool,
) *dispatchState {
	if masker == nil {
		masker = secrets.NewMasker()
	}
	var sem chan struct{}
	if maxParallel > 0 {
		sem = make(chan struct{}, maxParallel)
	}
	s := &dispatchState{
		sem:            sem,
		ctx:            ctx,
		backends:       backends,
		runner:         r,
		runID:          runID,
		pipeline:       pipeline,
		plan:           plan,
		delegate:       delegate,
		retryOf:        retryOf,
		masker:         masker,
		doneCh:         map[string]chan struct{}{},
		outputsJS:      map[string][]byte{},
		outcomes:       map[string]sparkwing.Outcome{},
		errors:         map[string]string{},
		failures:       map[string]sparkwing.Failure{},
		starts:         map[string]time.Time{},
		durations:      map[string]time.Duration{},
		claimedBy:      map[string]string{},
		scheduled:      map[string]*sparkwing.JobNode{},
		debug:          debug,
		admissionWaits: newAdmissionWaitTracker(),
	}
	if ipr, ok := r.(*NodeExecutor); ok {
		s.inlineRunner = ipr
	} else {
		s.inlineRunner = NewNodeExecutor(backends)
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
	s.resolverCtx = withLocalAdmission(s.resolverCtx, admission, leaseToken, leaseChildToken, leaseHostAdmitted, s.plan.PriorityValue())
	s.resolverCtx = withAdmissionWaitTracker(s.resolverCtx, s.admissionWaits)
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
		watchdogWaits := admissionWaitTrackerFromContext(ctx)
		watchdogParticipant := admissionWaitParticipantFromContext(ctx)
		awaitBounded := childAwaitBounded(ctx, req.Timeout)
		if awaitBounded {
			watchdogWaits.begin(watchdogParticipant)
			defer watchdogWaits.end(watchdogParticipant)
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
			payload = maskEventPayload(s.masker, payload)
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
			payload = maskEventPayload(s.masker, payload)
			if ev := s.backends.State.AppendEvent(ctx, s.runID, currentNode,
				"child_run_start", payload); ev != nil {
				sparkwing.Warn(ctx, "child_run_start audit event append failed: %v", ev)
			}
		}

		resumeProgressTimeout := pauseProgressTimeout(ctx)
		defer resumeProgressTimeout()
		pollCtx := ctx
		parentCtx := nodeParentContextFromContext(ctx)
		if req.Timeout > 0 {
			var cancel context.CancelFunc
			pollCtx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}
		timeoutPausedForAdmission := false
		timeoutAdjustedForAdmission := false
		watchdogPausedForAdmission := false
		var admissionStatusErr error
		nodeTimeout := nodeTimeoutControllerFromContext(ctx)
		var admissionMu sync.Mutex
		defer func() {
			admissionMu.Lock()
			paused := watchdogPausedForAdmission
			watchdogPausedForAdmission = false
			admissionMu.Unlock()
			if paused {
				watchdogWaits.end(watchdogParticipant)
			}
		}()
		updateTimeoutForAdmission := func(statusCtx context.Context) bool {
			trackNodeTimeout := req.Timeout == 0 && nodeTimeout != nil && nodeTimeoutDurationFromContext(ctx) > 0
			trackWatchdogAdmission := !awaitBounded && watchdogWaits != nil && watchdogParticipant != ""
			if !trackNodeTimeout && !trackWatchdogAdmission {
				return false
			}
			if statusCtx.Err() != nil {
				return false
			}
			la, _, _ := localAdmissionFromContext(ctx)
			admission, statusErr := childAdmissionStatus(statusCtx, s.backends.State, s.backends.Concurrency, la, childRunID)
			admissionMu.Lock()
			defer admissionMu.Unlock()
			if timeoutAdjustedForAdmission {
				return false
			}
			if statusErr != nil {
				if timeoutPausedForAdmission || watchdogPausedForAdmission {
					admissionStatusErr = statusErr
				}
				return false
			}
			if trackWatchdogAdmission {
				switch admission.Status {
				case childPlanAdmissionQueued:
					if !watchdogPausedForAdmission {
						watchdogWaits.begin(watchdogParticipant)
						watchdogPausedForAdmission = true
					}
				case childPlanAdmissionAdmitted:
					if watchdogPausedForAdmission {
						watchdogWaits.end(watchdogParticipant)
						watchdogPausedForAdmission = false
					}
				}
			}
			if !trackNodeTimeout {
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
			return timeoutPausedForAdmission || watchdogPausedForAdmission
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

func childAwaitBounded(ctx context.Context, requestTimeout time.Duration) bool {
	_, contextBounded := ctx.Deadline()
	return requestTimeout > 0 || nodeTimeoutDurationFromContext(ctx) > 0 || contextBounded
}

func repoSuffix(repo string) string {
	if repo == "" {
		return ""
	}
	return " repo=" + repo
}

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

func (s *dispatchState) resolveJSON(id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.outputsJS[id]
	return v, ok
}

func (s *dispatchState) setOutput(id string, v any) error {
	b, isBytes := v.([]byte)
	if !isBytes {
		m, err := json.Marshal(v)
		if err != nil {
			return nodeOutputMarshalError(id, v, err)
		}
		b = m
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outputsJS[id] = b
	return nil
}

func (s *dispatchState) setOutcome(id string, o sparkwing.Outcome) {
	s.mu.Lock()
	s.outcomes[id] = o
	if started, ok := s.starts[id]; ok {
		s.durations[id] = time.Since(started)
	}
	s.mu.Unlock()
	s.admissionWaits.signal()
}

func (s *dispatchState) setError(id, msg string) {
	s.mu.Lock()
	s.errors[id] = msg
	s.mu.Unlock()
}

func (s *dispatchState) setFailure(id string, f sparkwing.Failure) {
	s.mu.Lock()
	s.failures[id] = f
	s.mu.Unlock()
}

func (s *dispatchState) getFailure(id string) sparkwing.Failure {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[id]
}

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

func (s *dispatchState) markStarted(id string) {
	s.mu.Lock()
	if _, ok := s.starts[id]; !ok {
		s.starts[id] = time.Now()
	}
	s.mu.Unlock()
	s.admissionWaits.signal()
}

func (s *dispatchState) getOutcome(id string) (sparkwing.Outcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[id]
	return o, ok
}

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

func (s *dispatchState) invokeGenerator(exp sparkwing.Expansion) (out []*sparkwing.JobNode, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	out = exp.Gen(s.resolverCtx)
	return out, nil
}

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
	runnerCtx = withAdmissionWaitParticipant(runnerCtx, node.ID())

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
				Pipeline:            s.pipeline,
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

var defaultPauseTimeout = 30 * time.Minute

func pauseTimeout() time.Duration {
	if v := os.Getenv("SPARKWING_PAUSE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultPauseTimeout
}

const debugPausePollInterval = 500 * time.Millisecond

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

	ticker := time.NewTicker(debugPausePollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Until(pause.ExpiresAt))
	defer deadline.Stop()
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
		case <-deadline.C:
		}
	}
	// safety: "pending" is safe here; StartNode promotes it and FinishNode overwrites it.
	_ = s.backends.State.SetNodeStatus(s.ctx, s.runID, nodeID, "pending")
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_resumed", nil)
	return false
}

func (s *dispatchState) applyResult(nodeID string, res runner.Result) {
	recordNodeUsage(s.ctx, s.backends, s.runID, nodeID, res.Usage)
	if res.Output != nil {
		if err := s.setOutput(nodeID, res.Output); err != nil {
			res.Outcome = sparkwing.Failed
			res.Err = err
			// safety: the store cannot reopen the runner's terminal row, so this
			// in-memory correction must govern downstream scheduling.
			_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_failed", []byte(err.Error()))
		}
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

	ticker := time.NewTicker(approvalPollInterval())
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

func (s *dispatchState) pollApproval(nodeID string, deadline time.Time, onTimeout string, ticker *time.Ticker) approvalResult {
	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		return approvalResult{outcome: sparkwing.Failed, errMsg: err.Error()}
	}
	var deadlineC <-chan time.Time
	if !deadline.IsZero() {
		deadlineTimer := time.NewTimer(time.Until(deadline))
		defer deadlineTimer.Stop()
		deadlineC = deadlineTimer.C
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
		case <-deadlineC:
		case <-s.ctx.Done():
			return approvalResult{outcome: sparkwing.Cancelled, errMsg: "ctx-cancelled"}
		}
	}
}

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

func (s *dispatchState) invokeRecoveryRunner(node *sparkwing.JobNode, parentFailure sparkwing.Failure) runner.Result {
	ctx := sparkwing.WithFailure(s.resolverCtx, parentFailure)
	ctx = withAdmissionWaitParticipant(ctx, node.ID())
	return s.runWithCap(node, func(slot *workerSlot) runner.Result {
		return s.runner.RunNode(ctx, runner.Request{
			RunID:               s.runID,
			NodeID:              node.ID(),
			Pipeline:            s.pipeline,
			Node:                node,
			Delegate:            s.delegate,
			ReleaseWorkerSlot:   slot.release,
			ReacquireWorkerSlot: slot.reacquire,
		})
	})
}

type workerSlot struct {
	sem  chan struct{}
	ctx  context.Context
	mu   sync.Mutex
	held bool
}

func (w *workerSlot) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sem != nil && w.held {
		<-w.sem
		w.held = false
	}
}

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

func callBeforeRun(ctx context.Context, hook sparkwing.BeforeRunFn) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return hook(ctx)
}

func callAfterRun(ctx context.Context, hook sparkwing.AfterRunFn, runErr error, index int, nlog NodeLog) {
	defer func() {
		if r := recover(); r != nil {
			nlog.Log("error", fmt.Sprintf("AfterRun hook %d panicked: %v", index, r))
		}
	}()
	hook(ctx, runErr)
}

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

func (s *dispatchState) markFailed(nodeID string, reason error) {
	text := boundedFailureText(s.ctx, s.runID, nodeID, reason)
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Failed), text, nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_failed", []byte(text))
	appendFailureExcerptEvent(s.ctx, s.backends.State, s.runID, nodeID, reason)
	s.setOutcome(nodeID, sparkwing.Failed)
}

func (s *dispatchState) markCancelled(nodeID, reason string) {
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Cancelled), reason, nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_cancelled", []byte(reason))
	s.setOutcome(nodeID, sparkwing.Cancelled)
}

func (s *dispatchState) markRunCancelled(nodeID string) {
	ctx := context.WithoutCancel(s.ctx)
	_ = s.backends.State.FinishNode(ctx, s.runID, nodeID, string(sparkwing.Cancelled), "cancelled: run failing", nil)
	_ = s.backends.State.AppendEvent(ctx, s.runID, nodeID, "node_cancelled", []byte("cancelled: run failing"))
	s.setOutcome(nodeID, sparkwing.Cancelled)
}

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

const defaultPredicateTimeout = 30 * time.Second

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

type planSnapshot struct {
	Pipeline  string         `json:"pipeline"`
	RunID     string         `json:"run_id"`
	Priority  int            `json:"priority,omitempty"`
	Nodes     []snapshotNode `json:"nodes"`
	PlanConc  *snapshotConc  `json:"plan_concurrency,omitempty"`
	PlanConcs []snapshotConc `json:"plan_concurrency_groups,omitempty"`

	Resources *snapshotResources `json:"plan_resources,omitempty"`

	Secrets pipelines.SecretsField `json:"secrets,omitempty"`
}

type snapshotNode struct {
	ID   string            `json:"id"`
	Deps []string          `json:"deps"`
	Env  map[string]string `json:"env,omitempty"`

	Groups   []string          `json:"groups,omitempty"`
	Dynamic  bool              `json:"dynamic,omitempty"`
	Approval *snapshotApproval `json:"approval,omitempty"`

	OnFailureOf string `json:"on_failure_of,omitempty"`

	Modifiers *snapshotModifiers `json:"modifiers,omitempty"`

	Work *snapshotWork `json:"work,omitempty"`
}

type snapshotConc struct {
	Key string `json:"key,omitempty"`
}

type snapshotResources struct {
	Cores       float64 `json:"cores,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
}

type snapshotApproval struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

type snapshotModifiers struct {
	Retry               int      `json:"retry,omitempty"`
	RetryBackoffMS      int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto           bool     `json:"retry_auto,omitempty"`
	TimeoutMS           int64    `json:"timeout_ms,omitempty"`
	NoProgressTimeoutMS int64    `json:"no_progress_timeout_ms,omitempty"`
	RunsOn              []string `json:"runs_on,omitempty"`
	Prefers             []string `json:"prefers,omitempty"`
	WhenRunner          []string `json:"when_runner,omitempty"`

	Cache      bool  `json:"cache,omitempty"`
	CacheTTLMS int64 `json:"cache_ttl_ms,omitempty"`

	ConcGroup           string `json:"conc_group,omitempty"`
	ConcCapacity        int    `json:"conc_capacity,omitempty"`
	ConcCost            int    `json:"conc_cost,omitempty"`
	ConcScope           string `json:"conc_scope,omitempty"`
	ConcOnLimit         string `json:"conc_on_limit,omitempty"`
	ConcQueueTimeoutMS  int64  `json:"conc_queue_timeout_ms,omitempty"`
	ConcCancelTimeoutMS int64  `json:"conc_cancel_timeout_ms,omitempty"`

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

type snapshotWork struct {
	Steps         []snapshotStep      `json:"steps,omitempty"`
	Spawns        []snapshotSpawn     `json:"spawns,omitempty"`
	SpawnEach     []snapshotSpawnEach `json:"spawn_each,omitempty"`
	StepGroups    []snapshotStepGroup `json:"step_groups,omitempty"`
	ResultStep    string              `json:"result_step,omitempty"`
	FailurePolicy string              `json:"failure_policy,omitempty"`
}

type snapshotStepGroup struct {
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members"`
}

type snapshotStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
	Finally   bool     `json:"finally,omitempty"`

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

type planSnapshotMeta struct {
	Secrets          pipelines.SecretsField
	PipelineRequires []string
}

func marshalPlanSnapshot(p *sparkwing.Plan, rc sparkwing.RunContext, meta planSnapshotMeta) ([]byte, error) {
	snap := planSnapshot{
		Pipeline: rc.Pipeline,
		RunID:    rc.RunID,
		Priority: p.PriorityValue(),
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

func planPriorityFromSnapshot(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var snap struct {
		Priority int `json:"priority,omitempty"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0
	}
	return snap.Priority
}

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

func nodeModifiersSnapshot(n *sparkwing.JobNode) *snapshotModifiers {
	rc := n.RetryConfig()
	m := snapshotModifiers{
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

func isZeroModifiers(m snapshotModifiers) bool {
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
		!m.HasSkipIf
}

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
	out := &snapshotWork{FailurePolicy: string(work.ParallelFailurePolicy())}
	if resultStep != nil {
		out.ResultStep = resultStep.ID()
	}
	for _, s := range work.Steps() {
		out.Steps = append(out.Steps, snapshotStep{
			ID:        s.ID(),
			Needs:     s.DepIDs(),
			IsResult:  s == resultStep,
			HasSkipIf: len(s.SkipPredicates()) > 0,
			Finally:   s.IsFinally(),
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

func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
}
