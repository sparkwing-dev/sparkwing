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
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/secrets"
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

	// MaxParallel caps concurrent node execution. Zero = unbounded
	// (cluster default); local mode sets NumCPU. The cap applies only
	// to active execution; dep-wait goroutines are uncapped.
	MaxParallel int

	// LogStore, when non-nil, replaces the default filesystem
	// LogBackend used by RunLocal.
	LogStore storage.LogStore

	// ArtifactStore receives the final run-state NDJSON dump
	// (runs/<runID>/state.ndjson) after RunLocal exits.
	ArtifactStore storage.ArtifactStore
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

	runID := opts.RunID
	if runID == "" {
		runID = newRunID()
	}

	trigger := opts.Trigger
	if trigger.Source == "" {
		trigger.Source = "manual"
	}

	// Untracked dispatches pass nil; ensure RunContext.Git is non-nil
	// so rc.Git.IsDirty(ctx) errors instead of panicking.
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
	// Same instance lives on Runtime().Git so SDK helpers
	// (docker.ComputeTags, sparkwing.Runtime().Git.SHA in user code)
	// see the trigger's view without re-shelling.
	sparkwing.SetGit(gitOpt)

	// Split Git.Repo "owner/name" so the run row keeps populating the
	// historical github_owner / github_repo columns the dashboard
	// reads. New code reads Git.Repo directly.
	owner, repo := sparkwing.GithubOwnerRepo(gitOpt.Repo)
	// Build the invocation snapshot once and use it for both the
	// store row (so runs status / runs receipt / dashboards can show
	// "how was this started" without scanning logs) and the
	// run_start envelope event (so the live JSONL stream carries the
	// same shape). Single source of truth.
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

	// Pre-register secret-marked Inputs values before any node runs.
	// Same masker is reused for resolver + log redaction.
	masker := secrets.NewMasker()
	for _, v := range reg.SecretValues(opts.Args) {
		masker.Register(v)
	}

	// Plan build (parse Args -> typed Inputs -> Plan). Failures fail
	// the run with no nodes dispatched.
	plan, err := reg.Invoke(ctx, opts.Args, rc)
	if err != nil {
		_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("plan: %v", err))
		return &Result{RunID: runID, Status: "failed", Error: err}, nil
	}

	// Snapshot only the DAG; outputs stream into the nodes table.
	snapshot, err := marshalPlanSnapshot(plan, rc)
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
			NeedsLabels: n.RunsOnLabels(),
		}); err != nil {
			_ = backends.State.FinishRun(ctx, runID, "failed", fmt.Sprintf("create node %s: %v", n.ID(), err))
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
	}

	validatePlanModifiers(opts.Delegate, plan)

	// --start-at / --stop-at must reference a real WorkStep id;
	// reject with a Levenshtein-suggesting message before the
	// orchestrator even emits run_start, so the operator's iteration
	// loop is "save -> wing X -> see typo error" not "save -> dispatch
	// -> watch run finish silently doing nothing useful."
	if opts.StartAt != "" || opts.StopAt != "" {
		if err := sparkwing.ValidateStepRange(plan, opts.StartAt, opts.StopAt); err != nil {
			_ = backends.State.FinishRun(ctx, runID, "failed", err.Error())
			return &Result{RunID: runID, Status: "failed", Error: err}, nil
		}
		ctx = sparkwing.WithStepRange(ctx, opts.StartAt, opts.StopAt)
	}
	// Install the dry-run mode flag on the run-wide ctx so every
	// Work executed under it routes through DryRunFn instead
	// of the apply Fn. Steps without a dry-run body soft-skip with
	// reason `no_dry_run_defined`.
	if opts.DryRun {
		ctx = sparkwing.WithDryRun(ctx)
	}

	emitRunStart(opts.Delegate, invocation)
	emitRunPlan(opts.Delegate, plan)

	r := opts.Runner
	if r == nil {
		r = NewInProcessRunner(backends)
	}
	// Lazy resolver: cache + masker installed only when SecretSource
	// is supplied. Masker is also stashed on ctx so loggers can pull
	// it without a signature change.
	ctx = secrets.WithMasker(ctx, masker)
	if opts.SecretSource != nil {
		ctx = sparkwing.WithSecretResolver(ctx,
			secrets.NewCached(opts.SecretSource, masker).AsResolver())
	}
	delegate := secrets.MaskingLogger(opts.Delegate, masker)

	// Local-only RunAndAwait trigger consumer; cluster mode
	// delegates this to the warm-runner pool.
	if ls, ok := backends.State.(localState); ok {
		consumerCtx, cancelConsumer := context.WithCancel(ctx)
		defer cancelConsumer()
		go runLocalTriggerLoop(consumerCtx, ls.st, runID, nil)
	}

	runErr := dispatch(ctx, backends, r, runID, plan, delegate, opts.Debug, opts.RetryOf, opts.Full, masker, opts.MaxParallel)

	finalStatus := "success"
	errMsg := ""
	if runErr != nil {
		finalStatus = "failed"
		errMsg = runErr.Error()
	}
	_ = backends.State.FinishRun(ctx, runID, finalStatus, errMsg)

	// Emit run_finish here so the envelope tee (installed by
	// RunLocal around opts.Delegate) captures it. Previously the
	// outer Main() in this package emitted run_finish AFTER RunLocal
	// returned -- which meant the envelope log closed before the
	// terminal event landed, and `runs logs --follow` could never
	// surface a "run finished" line. The Main() emission becomes the
	// one for callers that drove orchestrator.Run directly without
	// the envelope tee; we keep it idempotent there by checking the
	// presence of the EnvelopeLogger flag in the delegate chain.
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
				hints["retry"] = "sparkwing runs retry --run " + runID
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

// RunLocal opens the local store, wires LocalBackends, and runs.
// Defaults SecretSource to the laptop dotenv when nil.
func RunLocal(ctx context.Context, paths Paths, opts Options) (*Result, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, fmt.Errorf("ensure sparkwing root: %w", err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()
	if opts.SecretSource == nil {
		opts.SecretSource = secrets.NewDotenvSource("")
	}
	backends := LocalBackends(paths, st)
	if opts.LogStore != nil {
		backends.Logs = NewLogStoreBackend(opts.LogStore, nil)
	}
	// Wrap the user-facing delegate with an envelope tee so every
	// run-wide event (run_start, run_plan, node_start, node_end,
	// run_summary, run_finish, plan_warn, exec_line, ...) is also
	// persisted to <runDir>/_envelope.ndjson. The merged-stream reader
	// in JobLogs replays this file alongside per-node body output so
	// `runs logs --follow` reconstructs the full chronological event
	// stream that today only the dispatcher's stdout sees.
	//
	// We need the run id to derive the envelope path, but RunLocal
	// generates the id when opts.RunID is empty. Mint it here so the
	// inner Run() honors it AND the envelope file lives at the right
	// directory.
	if opts.RunID == "" {
		opts.RunID = newRunID()
	}
	if err := paths.EnsureRunDir(opts.RunID); err != nil {
		return nil, fmt.Errorf("ensure run dir: %w", err)
	}
	envLog, envErr := newEnvelopeLogger(paths.EnvelopeLog(opts.RunID), opts.Delegate)
	if envErr == nil {
		opts.Delegate = envLog
		defer envLog.Close()
	}
	res, runErr := Run(ctx, backends, opts)
	// Dump on error too, for post-mortem of partial runs.
	if opts.ArtifactStore != nil && res != nil && res.RunID != "" {
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
func dispatch(ctx context.Context, backends Backends, r runner.Runner, runID string, plan *sparkwing.Plan, delegate sparkwing.Logger, debug DebugDirectives, retryOf string, full bool, masker *secrets.Masker, maxParallel int) error {
	runStart := time.Now()

	// Plan-level .Cache() gates the whole run before any dispatch.
	planRelease, planOutcome, perr := acquirePlanSlot(ctx, backends, runID, plan)
	if perr != nil {
		return perr
	}
	switch planOutcome {
	case planCacheSkipped:
		return nil // run-level success; no nodes ran
	case planCacheFailed:
		return fmt.Errorf("plan concurrency key %q: slot full under OnLimit:Fail", plan.CacheOpts().Key)
	case planCacheEvicted:
		return fmt.Errorf("plan concurrency key %q: evicted before dispatch", plan.CacheOpts().Key)
	}
	planReleaseOutcome := "success"
	defer func() { planRelease(planReleaseOutcome) }()

	state := newDispatchState(ctx, backends, r, runID, plan, delegate, debug, retryOf, masker, maxParallel)

	// Skip-passed: pre-seed succeeded nodes from the prior run so
	// runOneNode short-circuits them.
	if retryOf != "" && !full {
		state.rehydrateFromRetry(ctx, retryOf)
	}

	// Seed with the plan's static nodes.
	seen := make(map[string]bool, len(plan.Nodes()))
	for _, n := range plan.Nodes() {
		state.scheduleNode(n)
		seen[n.ID()] = true
	}

	// Detached OnFailure recoveries don't appear in plan.Nodes() but
	// need a row + goroutine to wait on the parent's doneCh.
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
			NeedsLabels: rec.RunsOnLabels(),
		})
		state.scheduleNode(rec)
		seen[rec.ID()] = true
	}

	for _, exp := range plan.Expansions() {
		state.scheduleExpansion(exp)
	}

	state.wg.Wait()

	// Optional nodes don't propagate failure to run-level.
	var failed []string
	for _, n := range plan.Nodes() {
		oc, ok := state.getOutcome(n.ID())
		if !ok || oc.OK() {
			continue
		}
		if n.IsOptional() {
			continue
		}
		failed = append(failed, n.ID())
	}

	emitRunSummary(delegate, plan, state, runStart, len(failed) == 0)

	if len(failed) > 0 {
		planReleaseOutcome = "failed"
		return fmt.Errorf("nodes failed: %v", failed)
	}
	return nil
}

// validatePlanModifiers warns on combinations that silently no-op.
func validatePlanModifiers(delegate sparkwing.Logger, plan *sparkwing.Plan) {
	if delegate == nil {
		return
	}
	for _, n := range plan.Nodes() {
		if n.IsInline() && len(n.RunsOnLabels()) > 0 {
			delegate.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "warn",
				Node:  n.ID(),
				Event: "plan_warn",
				Msg:   "Inline() and RunsOn() are set on the same node — RunsOn labels are ignored for inline execution",
				Attrs: map[string]any{
					"inline":      true,
					"runs_on":     n.RunsOnLabels(),
					"ignored_key": "runs_on",
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
//
// Trigger env values are deliberately omitted; only the names are
// surfaced because the values can carry secrets pipelines pull at
// runtime via TriggerEnv(...).
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
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
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
	if len(opts.Trigger.Env) > 0 {
		keys := make([]string, 0, len(opts.Trigger.Env))
		for k := range opts.Trigger.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		inv["trigger_env_keys"] = keys
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

// buildRunFlags returns the operator-facing wing flags that influenced
// this run. Empty/zero-valued fields are omitted so the resulting map
// reads as "what's non-default about this invocation". The shape
// matches the wing CLI flag names so an agent can echo the map back
// into a re-invocation.
//
// Some flags (allow-destructive, allow-prod, allow-money) are
// consumed by the wing wrapper itself for the blast-radius gate and
// never reach Options. We pick those up via the SPARKWING_ALLOW_*
// env vars the wrapper forwards specifically so they show up here.
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
	if os.Getenv("SPARKWING_ALLOW_DESTRUCTIVE") == "1" {
		flags["allow_destructive"] = true
	}
	if os.Getenv("SPARKWING_ALLOW_PROD") == "1" {
		flags["allow_prod"] = true
	}
	if os.Getenv("SPARKWING_ALLOW_MONEY") == "1" {
		flags["allow_money"] = true
	}
	// Wing-side flags forwarded only for the run-record breadcrumb.
	// `from` / `config` / `no_update` are consumed by the wing
	// wrapper before exec, so the pipeline binary never lifts them
	// onto Options -- but knowing they were set is still load-bearing
	// for reproducibility.
	if v := os.Getenv("SPARKWING_FROM"); v != "" {
		flags["from"] = v
	}
	if v := os.Getenv("SPARKWING_CONFIG"); v != "" {
		flags["config"] = v
	}
	if os.Getenv("SPARKWING_NO_UPDATE") == "1" {
		flags["no_update"] = true
	}
	if v := os.Getenv("SPARKWING_ON"); v != "" {
		flags["on"] = v
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

// buildReproducer assembles a `wing <pipeline> [flags] [args]` shell
// command that re-runs this invocation. Args use --key=value form so
// values containing spaces don't need extra escaping; consumers that
// need shell-quoting can run the result through their own escaper.
// The retry-of flag is included only when this run was itself a
// retry (i.e. opts.RetryOf is set); a fresh agent reproducing should
// pick whether to retry-of the failed run themselves.
func buildReproducer(opts Options, _ string) string {
	parts := []string{"wing", opts.Pipeline}
	flagKeys := make([]string, 0)
	flags := buildRunFlags(opts)
	for k := range flags {
		flagKeys = append(flagKeys, k)
	}
	sort.Strings(flagKeys)
	for _, k := range flagKeys {
		// max_parallel maps to --workers (wing flag name); skip
		// when it equals NumCPU since that's the default and would
		// make every reproducer noisy with the local machine's CPU
		// count. Agents wanting a precise replay can read the
		// structured attrs.flags map.
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
		if gs := plan.NodeGroupNames(n.ID()); len(gs) > 0 {
			row["groups"] = gs
		}
		// ExpandFrom fan-in edges: expose the source so the plan
		// preview draws the edge.
		if srcs := plan.GroupSourceIDs(n.ID()); len(srcs) > 0 {
			row["group_deps"] = srcs
		}
		if w := n.Work(); w != nil {
			workSteps := w.Steps()
			// Suppress the synthetic single "run" step that single-
			// closure Jobs produce -- the node line already conveys
			// everything in that case.
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
func planTopologyHash(nodes []*sparkwing.Node) string {
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
	nodes := plan.Nodes()
	rows := make([]any, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	appendRow := func(id string, outcome string) {
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
		// Include OnFailure recoveries adjacent to their parent only
		// when they actually ran. Guard against duplicates from
		// registering the recovery via both Plan.Add and .OnFailure.
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
	ctx         context.Context
	resolverCtx context.Context
	backends    Backends
	runner      runner.Runner
	runID       string
	plan        *sparkwing.Plan
	delegate    sparkwing.Logger

	mu        sync.Mutex
	doneCh    map[string]chan struct{} // per-node completion signal
	outputs   map[string]any           // per-node typed output (in-process runner)
	outputsJS map[string][]byte        // per-node raw JSON output (cluster runner)
	outcomes  map[string]sparkwing.Outcome
	errors    map[string]string        // per-node error message, set when runner.Result.Err is non-nil
	starts    map[string]time.Time     // per-node wall-clock start, stamped at runOneNode entry
	durations map[string]time.Duration // per-node wall-clock duration, computed when outcome is recorded
	claimedBy map[string]string        // recoveryID -> parentID (OnFailure)

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

	wg sync.WaitGroup
}

func newDispatchState(ctx context.Context, backends Backends, r runner.Runner, runID string, plan *sparkwing.Plan, delegate sparkwing.Logger, debug DebugDirectives, retryOf string, masker *secrets.Masker, maxParallel int) *dispatchState {
	if masker == nil {
		masker = secrets.NewMasker()
	}
	var sem chan struct{}
	if maxParallel > 0 {
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
		starts:    map[string]time.Time{},
		durations: map[string]time.Duration{},
		claimedBy: map[string]string{},
		debug:     debug,
	}
	if ipr, ok := r.(*InProcessRunner); ok {
		s.inlineRunner = ipr
	} else {
		s.inlineRunner = NewInProcessRunner(backends)
	}
	// OnFailure claims only come from initial plan nodes.
	for _, n := range plan.Nodes() {
		if rec := n.OnFailureNode(); rec != nil {
			s.claimedBy[rec.ID()] = n.ID()
		}
	}
	// Outer fallback logger for SDK-internal Debug/Log calls that
	// happen before the per-node nodeLogger opens.
	if delegate != nil {
		s.resolverCtx = sparkwing.WithLogger(ctx, delegate)
	} else {
		s.resolverCtx = ctx
	}
	s.resolverCtx = sparkwing.WithResolver(s.resolverCtx, s.resolve)
	s.resolverCtx = sparkwing.WithJSONResolver(s.resolverCtx, s.resolveJSON)
	s.resolverCtx = sparkwing.WithPipelineResolver(s.resolverCtx, s.pipelineRef())
	s.resolverCtx = sparkwing.WithPipelineAwaiter(s.resolverCtx, s.pipelineAwaiter())
	// Install the typed Inputs the registration parsed so step
	// bodies can read the value via sparkwing.Inputs[T](ctx).
	if in := plan.Inputs(); in != nil {
		s.resolverCtx = sparkwing.WithInputs(s.resolverCtx, in)
	}
	return s
}

// pipelineAwaiter enqueues a child trigger, polls until terminal,
// returns the target node's output bytes.
func (s *dispatchState) pipelineAwaiter() sparkwing.PipelineAwaiter {
	return sparkwing.PipelineAwaiterFunc(func(ctx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
		currentNode := sparkwing.NodeFromContext(ctx)

		// Retry-lineage chain. When this run is itself a retry
		// (s.retryOf != ""), look up the prior run's child trigger
		// spawned at the same node + pipeline. If found, thread its
		// id as the new child's retry_of so the child gets skip-
		// passed treatment too. No match = the prior run never
		// reached this spawn point; new child runs fresh.
		var childRetryOf string
		if s.retryOf != "" && currentNode != "" {
			id, ferr := s.backends.State.FindSpawnedChildTriggerID(ctx, s.retryOf, currentNode, req.Pipeline)
			if ferr != nil {
				sparkwing.Warn(ctx, "find prior spawned child for retry chain: %v", ferr)
			} else {
				childRetryOf = id
			}
		}

		childRunID, err := s.backends.State.EnqueueTrigger(ctx,
			req.Pipeline, req.Args, s.runID, currentNode, childRetryOf,
			"await-pipeline", "", req.Repo, req.Branch)
		if err != nil {
			return nil, fmt.Errorf("enqueue trigger: %w", err)
		}

		// Otherwise long awaits look like dead air.
		sparkwing.Info(ctx,
			"spawned child run %s (pipeline=%s%s)",
			childRunID, req.Pipeline, repoSuffix(req.Repo))

		if currentNode != "" {
			payload, _ := json.Marshal(map[string]any{
				"pipeline":        req.Pipeline,
				"node_id":         req.NodeID,
				"child_run_id":    childRunID,
				"timeout_seconds": int64(req.Timeout.Seconds()),
			})
			if ev := s.backends.State.AppendEvent(ctx, s.runID, currentNode,
				"pipeline_await_spawned", payload); ev != nil {
				sparkwing.Warn(ctx, "pipeline_await audit event append failed: %v", ev)
			}
		}

		pollCtx := ctx
		if req.Timeout > 0 {
			var cancel context.CancelFunc
			pollCtx, cancel = context.WithTimeout(ctx, req.Timeout)
			defer cancel()
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		// Heartbeat surfaces "still alive" so long awaits don't look
		// wedged.
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		startedAt := time.Now()
		lastStatus := "pending"
		for {
			run, err := s.backends.State.GetRun(pollCtx, childRunID)
			if err == nil {
				lastStatus = run.Status
				switch run.Status {
				case "success":
					// Empty NodeID = caller wants only success, no output.
					if req.NodeID == "" {
						return &sparkwing.ResolvedPipelineRef{RunID: childRunID}, nil
					}
					data, oerr := s.backends.State.GetNodeOutput(pollCtx, childRunID, req.NodeID)
					if oerr != nil {
						return nil, fmt.Errorf("get child %s/%s output: %w", childRunID, req.NodeID, oerr)
					}
					return &sparkwing.ResolvedPipelineRef{RunID: childRunID, Data: data}, nil
				case "failed":
					return nil, fmt.Errorf("child run %s failed: %s", childRunID, run.Error)
				case "cancelled":
					return nil, fmt.Errorf("child run %s was cancelled", childRunID)
				}
			}
			select {
			case <-pollCtx.Done():
				return nil, fmt.Errorf("waiting for child %s: %w", childRunID, pollCtx.Err())
			case <-heartbeat.C:
				sparkwing.Info(ctx,
					"still waiting on child %s [%s] (status=%s, elapsed=%s)",
					childRunID, req.Pipeline, lastStatus,
					time.Since(startedAt).Round(time.Second))
			case <-ticker.C:
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
		// Best-effort audit event.
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
func (s *dispatchState) scheduleNode(node *sparkwing.Node) {
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
		exp.Group.Finalize(nil, fmt.Errorf("ctx cancelled before expansion"))
		return
	}

	// Only proceed if the source actually succeeded. If it didn't,
	// the expansion can't produce meaningful children; signal the
	// group with an error and let downstream cancel cleanly.
	oc, _ := s.getOutcome(exp.Source.ID())
	if !oc.OK() {
		exp.Group.Finalize(nil, fmt.Errorf("expansion source %q did not succeed (outcome=%s)", exp.Source.ID(), oc))
		return
	}

	children, err := s.invokeGenerator(exp)
	if err != nil {
		sparkwing.LoggerFromContext(s.resolverCtx).Log("error",
			fmt.Sprintf("ExpandFrom(%s) failed: %v", exp.Source.ID(), err))
		exp.Group.Finalize(nil, err)
		return
	}

	if err := s.plan.InsertExpanded(exp.Source, children); err != nil {
		exp.Group.Finalize(nil, err)
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
			NeedsLabels: child.RunsOnLabels(),
		}); err != nil {
			sparkwing.LoggerFromContext(s.resolverCtx).Log("error",
				fmt.Sprintf("ExpandFrom(%s): store child %s: %v", exp.Source.ID(), child.ID(), err))
		}
		s.scheduleNode(child)
	}
	// Snapshot now so dashboards see the expanded DAG.
	if snap, merr := marshalPlanSnapshot(s.plan, sparkwing.RunContext{Pipeline: "", RunID: s.runID}); merr == nil {
		_ = s.backends.State.UpdatePlanSnapshot(s.ctx, s.runID, snap)
	}

	// Backfill downstream waiter deps_json so jobs status shows the
	// dynamic membership.
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

	exp.Group.Finalize(children, nil)
}

// invokeGenerator runs the user closure under panic recovery.
func (s *dispatchState) invokeGenerator(exp sparkwing.Expansion) (out []*sparkwing.Node, err error) {
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
func (s *dispatchState) runOneNode(node *sparkwing.Node) {
	// Skip-passed short-circuit; rehydrateFromRetry already seeded.
	if _, prerendered := s.getOutcome(node.ID()); prerendered {
		return
	}
	// Recovery nodes wait for parent failure and bypass cache/SkipIf/
	// Exclusive — the runner gets the job-only path.
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
		// Recovery node about to invoke its runner -- this is the
		// real "started doing work" point.
		s.markStarted(node.ID())
		res := s.invokeRecoveryRunner(node)
		s.applyResult(node.ID(), res)
		return
	}

	// Dynamic groups first; they resolve into extra deps.
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

	// Optional deps absent from the plan are silently dropped.
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
		upstream := s.plan.Node(depID)
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

	// Approval gate bypasses the Runner; CreateApproval flips status
	// to approval_pending until human/timeout resolves.
	if node.IsApproval() {
		if reason, skip := evalSkipPredicates(s.resolverCtx, node); skip {
			s.markSkipped(node.ID(), reason)
			return
		}
		// Approval gate enters the wait state -- treat that as the
		// node's start. Approval pause time IS the node's runtime.
		s.markStarted(node.ID())
		res := s.runApprovalGate(node)
		s.applyResult(node.ID(), res)
		return
	}

	// Mark started just before the runner dispatch loop so cancelled-
	// by-upstream and skipped-by-predicate nodes (handled above) don't
	// inherit a duration from the time they spent waiting on deps.
	s.markStarted(node.ID())

	activeRunner := s.runner
	if node.IsInline() {
		activeRunner = s.inlineRunner
	}
	runnerCtx := sparkwing.WithSpawnHandler(s.resolverCtx, s.newSpawnHandler(node.ID()))

	// Node-level auto-retry: re-dispatch the whole runner on infra
	// flakes. Only Failed outcomes with a non-nil err are eligible.
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

		res = s.runWithCap(node, func() runner.Result {
			return activeRunner.RunNode(runnerCtx, runner.Request{
				RunID:    s.runID,
				NodeID:   node.ID(),
				Node:     node,
				Delegate: s.delegate,
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
	// 'pending' is safe: StartNode promotes; FinishNode overwrites.
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
	}
	s.setOutcome(nodeID, res.Outcome)
}

// runApprovalGate writes the approvals row and blocks until resolved.
// Approved -> Success; Denied -> Failed; timeout per ApprovalOnTimeout.
func (s *dispatchState) runApprovalGate(node *sparkwing.Node) runner.Result {
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
	defer nlog.Close()

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

	outcome, errMsg, out := s.pollApproval(node.ID(), deadline, onTimeout, ticker)

	nlog.Emit(sparkwing.LogRecord{
		TS:    time.Now(),
		Level: "info",
		Event: "node_end",
		Attrs: map[string]any{
			"outcome":     string(outcome),
			"duration_ms": time.Since(nodeStartTS).Milliseconds(),
		},
	})

	if err := s.backends.State.FinishNode(s.ctx, s.runID, node.ID(), string(outcome), errMsg, out); err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	if outcome == sparkwing.Failed && errMsg != "" {
		return runner.Result{Outcome: outcome, Err: errors.New(errMsg), Output: nil}
	}
	return runner.Result{Outcome: outcome}
}

// pollApproval blocks until a resolution appears or deadline fires.
// On deadline writes timed_out so a late human resolve becomes 409.
func (s *dispatchState) pollApproval(nodeID string, deadline time.Time, onTimeout string, ticker *time.Ticker) (sparkwing.Outcome, string, []byte) {
	for {
		got, err := s.backends.State.GetApproval(s.ctx, s.runID, nodeID)
		if err == nil && got.ResolvedAt != nil {
			return approvalResolutionToOutcome(got.Resolution, got.Approver, got.Comment)
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if _, err := s.backends.State.ResolveApproval(s.ctx, s.runID, nodeID,
				store.ApprovalResolutionTimedOut, "sparkwing", "timeout"); err != nil {
				if errors.Is(err, store.ErrLockHeld) {
					// Human beat us; use their resolution.
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
			return sparkwing.Cancelled, "ctx-cancelled", nil
		}
	}
}

// approvalResolutionToOutcome maps a stored resolution to outcome.
func approvalResolutionToOutcome(resolution, approver, comment string) (sparkwing.Outcome, string, []byte) {
	payload, _ := json.Marshal(map[string]any{
		"resolution": resolution,
		"approver":   approver,
		"comment":    comment,
	})
	switch resolution {
	case store.ApprovalResolutionApproved:
		return sparkwing.Success, "", payload
	case store.ApprovalResolutionDenied:
		msg := fmt.Sprintf("denied by %s", approver)
		if comment != "" {
			msg += ": " + comment
		}
		return sparkwing.Failed, msg, payload
	case store.ApprovalResolutionTimedOut:
		return sparkwing.Failed, "approval timed out", payload
	default:
		return sparkwing.Failed, "unknown approval resolution: " + resolution, payload
	}
}

// approvalTimeoutToOutcome applies the author-configured on_timeout.
func approvalTimeoutToOutcome(onTimeout string) (sparkwing.Outcome, string, []byte) {
	switch onTimeout {
	case store.ApprovalOnTimeoutApprove:
		return sparkwing.Success, "", nil
	case store.ApprovalOnTimeoutDeny:
		return sparkwing.Failed, "approval timed out (policy=deny)", nil
	default:
		return sparkwing.Failed, "approval timed out (policy=fail)", nil
	}
}

// invokeRecoveryRunner runs a recovery node via the in-process
// job-only path; cluster runners fall back to full RunNode.
func (s *dispatchState) invokeRecoveryRunner(node *sparkwing.Node) runner.Result {
	if ipr, ok := s.runner.(*InProcessRunner); ok {
		out, err := ipr.executeNode(s.resolverCtx, s.runID, node, s.delegate)
		if err != nil {
			return runner.Result{Outcome: sparkwing.Failed, Err: err}
		}
		return runner.Result{Outcome: sparkwing.Success, Output: out}
	}
	return s.runWithCap(node, func() runner.Result {
		return s.runner.RunNode(s.resolverCtx, runner.Request{
			RunID:    s.runID,
			NodeID:   node.ID(),
			Node:     node,
			Delegate: s.delegate,
		})
	})
}

// runWithCap gates non-inline RunNode against the MaxParallel sem.
// Nil sem or inline node = no cap.
func (s *dispatchState) runWithCap(node *sparkwing.Node, fn func() runner.Result) runner.Result {
	if s.sem == nil || node.IsInline() {
		return fn()
	}
	select {
	case s.sem <- struct{}{}:
	case <-s.resolverCtx.Done():
		return runner.Result{Outcome: sparkwing.Cancelled}
	}
	defer func() { <-s.sem }()
	return fn()
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

func (s *dispatchState) markSkipped(nodeID, reason string) {
	_ = s.backends.State.FinishNode(s.ctx, s.runID, nodeID, string(sparkwing.Skipped), reason, nil)
	_ = s.backends.State.AppendEvent(s.ctx, s.runID, nodeID, "node_skipped", []byte(reason))
	s.setOutcome(nodeID, sparkwing.Skipped)
}

// defaultPredicateTimeout caps a SkipIf predicate when the node
// doesn't override.
const defaultPredicateTimeout = 30 * time.Second

// evalSkipPredicates returns (reason, true) on the first true predicate.
// Errors/panics/timeouts don't skip — run the work and let the job decide.
func evalSkipPredicates(ctx context.Context, node *sparkwing.Node) (string, bool) {
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
	Pipeline string `json:"pipeline"`
	RunID    string `json:"run_id"`
	// Venue is the author-declared dispatch constraint surfaced at
	// the top of the plan snapshot. Agents reading --explain JSON
	// see the venue alongside the DAG so they can honor it without
	// a separate `--describe` round-trip.
	Venue string         `json:"venue,omitempty"`
	Nodes []snapshotNode `json:"nodes"`
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

// snapshotApproval is the wire shape of an approval gate's prompt +
// timeout policy.
type snapshotApproval struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

// snapshotModifiers is the wire shape of a Node's Plan-layer modifiers.
type snapshotModifiers struct {
	Retry           int      `json:"retry,omitempty"`
	RetryBackoffMS  int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto       bool     `json:"retry_auto,omitempty"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	RunsOn          []string `json:"runs_on,omitempty"`
	CacheKey        string   `json:"cache_key,omitempty"`
	CacheMax        int      `json:"cache_max,omitempty"`
	CacheOnLimit    string   `json:"cache_on_limit,omitempty"`
	Inline          bool     `json:"inline,omitempty"`
	Optional        bool     `json:"optional,omitempty"`
	ContinueOnError bool     `json:"continue_on_error,omitempty"`
	OnFailure       string   `json:"on_failure,omitempty"`
	HasBeforeRun    bool     `json:"has_before_run,omitempty"`
	HasAfterRun     bool     `json:"has_after_run,omitempty"`
	HasSkipIf       bool     `json:"has_skip_if,omitempty"`
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
	// BlastRadius is the author-declared marker set on this step,
	// stringified to canonical wire tokens. Empty when
	// no marker was declared. Surfaced in the plan snapshot so
	// `pipeline explain --json` consumers (agents, dashboard) see
	// the contract alongside the static DAG.
	BlastRadius []string `json:"blast_radius,omitempty"`
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

func marshalPlanSnapshot(p *sparkwing.Plan, rc sparkwing.RunContext) ([]byte, error) {
	snap := planSnapshot{
		Pipeline: rc.Pipeline,
		RunID:    rc.RunID,
	}
	// Surface the registered venue at the snapshot top so agents
	// reading --explain JSON honor the dispatch constraint
	// without a separate --describe round-trip. Best-effort lookup --
	// synthetic test fixtures with a Plan but no Registration just
	// emit no venue field (json:"omitempty").
	if rc.Pipeline != "" {
		if reg, ok := sparkwing.Lookup(rc.Pipeline); ok {
			snap.Venue = sparkwing.PipelineVenue(reg).String()
		}
	}
	// Cycle detection threads through the snapshot walk to catch
	// A->B->A loops in one pass.
	walker := newWorkWalker()
	// Dedupe nodes that are both plan.Add'd and OnFailure-attached.
	seen := make(map[string]bool)
	for _, n := range p.Nodes() {
		sn := snapshotNode{
			ID:      n.ID(),
			Deps:    n.DepIDs(),
			Env:     n.EnvMap(),
			Groups:  p.NodeGroupNames(n.ID()),
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
	// OnFailure recovery nodes are attached via `.OnFailure(id, job)`
	// and constructed detached, so they aren't in plan.Nodes(). Emit
	// a snapshot entry for each unseen recovery so the dashboard can
	// draw the failure-branch edge and the DAG layout can treat
	// `on_failure_of` as a virtual dep for column placement.
	for _, n := range p.Nodes() {
		rec := n.OnFailureNode()
		if rec == nil || seen[rec.ID()] {
			continue
		}
		recSnap := snapshotNode{
			ID:          rec.ID(),
			Deps:        rec.DepIDs(),
			Env:         rec.EnvMap(),
			Groups:      p.NodeGroupNames(rec.ID()),
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

// nodeModifiersSnapshot extracts the Plan-layer modifiers a renderer
// cares about. Returns nil when nothing is set so JSON omits the
// field entirely.
func nodeModifiersSnapshot(n *sparkwing.Node) *snapshotModifiers {
	rc := n.RetryConfig()
	m := snapshotModifiers{
		Retry:           rc.Attempts,
		RetryBackoffMS:  rc.Backoff.Milliseconds(),
		RetryAuto:       rc.Auto,
		TimeoutMS:       n.TimeoutDuration().Milliseconds(),
		RunsOn:          n.RunsOnLabels(),
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
	if c := n.CacheOpts(); c.HasKey() {
		m.CacheKey = c.Key
		m.CacheMax = c.Max
		m.CacheOnLimit = string(c.OnLimit)
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
		m.CacheKey == "" &&
		m.CacheMax == 0 &&
		m.CacheOnLimit == "" &&
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
		var br []string
		if markers := s.BlastRadius(); len(markers) > 0 {
			br = make([]string, len(markers))
			for i, m := range markers {
				br[i] = m.String()
			}
		}
		out.Steps = append(out.Steps, snapshotStep{
			ID:          s.ID(),
			Needs:       s.DepIDs(),
			IsResult:    s == resultStep,
			HasSkipIf:   len(s.SkipPredicates()) > 0,
			BlastRadius: br,
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
		// Render an item template by invoking fn with a zero-value
		// input; closures that panic on zero fall back to a Note.
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
