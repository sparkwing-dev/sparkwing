package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const SubmitRequestIDKey = "_SPARKWING_SUBMIT_REQUEST_ID"

// submitTriggerSourcePrefix keeps the value written before `runs submit` folded
// into `run --sw-detached`, so stored rows and `runs find` queries still match.
const submitTriggerSourcePrefix = "runs-submit"

const detachedPath = "run --sw-detached"

type submitResult struct {
	orchestrator.RunHandle

	AlreadySubmitted bool `json:"already_submitted,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
	RequestID      string `json:"request_id,omitempty"`

	ConsumerPID     int    `json:"consumer_pid,omitempty"`
	ConsumerStarted string `json:"consumer_started,omitempty"`
}

// runDetached persists PIPELINE as a trigger, hands it to this home's resident
// consumer, and prints the run handle. Nothing about the run is bound to this
// process afterward.
func runDetached(ctx context.Context, pipelineName string, wf runFlags, passthrough []string) error {
	if err := refuseForegroundOnlyFlags(wf); err != nil {
		return err
	}
	format, err := resolveDetachedOutput(wf.outputFormat)
	if err != nil {
		return err
	}
	idle, err := parseConsumerDuration("--sw-consumer-idle", wf.consumerIdle)
	if err != nil {
		return err
	}
	claimLease, err := parseConsumerDuration("--sw-consumer-claim-lease", wf.consumerClaimLease)
	if err != nil {
		return err
	}

	//nolint:contextcheck // The repo registry read predates a context-aware API.
	repoDir, declared, err := resolveSubmitRepo(ctx, pipelineName, wf.changeDir, wf.pipelineRef)
	if err != nil {
		return err
	}

	priority := ""
	if strings.TrimSpace(wf.priority) != "" {
		priority, err = validatePriorityFlag(wf.priority)
		if err != nil {
			return err
		}
	}

	handlePath := ""
	if wf.runHandleFile != "" {
		handlePath, err = filepath.Abs(wf.runHandleFile)
		if err != nil {
			return fmt.Errorf("--sw-run-handle-file %s: %w", wf.runHandleFile, err)
		}
		release, rerr := orchestrator.ReserveRunHandle(handlePath)
		if rerr != nil {
			return fmt.Errorf("--sw-run-handle-file %s: %w", wf.runHandleFile, rerr)
		}
		defer release()
	}

	// safety: a detached launch takes no home flag; SPARKWING_HOME selects it.
	paths, err := submitPaths("")
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure %s: %w", paths.Root, err)
	}
	//nolint:contextcheck // Store.Open owns its bounded migration context and has no caller-context variant.
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open %s: %w", paths.StateDB(), err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			slog.Default().Warn("close runs store", "path", paths.StateDB(), "error", cerr)
		}
	}()

	result, err := persistSubmission(ctx, st, paths, submission{
		Pipeline: pipelineName,
		Args:     collectPipelineArgs(passthrough),
		RepoDir:  repoDir,
		Gate: riskGate{
			Surface:   detachedPath,
			Pipeline:  pipelineName,
			Flags:     wf,
			SubmitDir: repoDir,
			Declared:  declared,
		}.check,
		Ref:            strings.TrimSpace(wf.ref),
		PipelineRef:    strings.TrimSpace(wf.pipelineRef),
		Priority:       priority,
		IdempotencyKey: strings.TrimSpace(wf.idempotencyKey),
		RequestID:      strings.TrimSpace(wf.requestID),
	})
	if err != nil {
		return err
	}

	// safety: published before the consumer can start the run, so a caller that
	// waits on the file never races the run it describes.
	if handlePath != "" {
		if perr := orchestrator.PublishRunHandle(handlePath, result.RunHandle); perr != nil {
			return fmt.Errorf("run %s is persisted but its handle could not be published to %s: %w",
				result.RunID, wf.runHandleFile, perr)
		}
	}

	if runNeedsDaemon(wf, passthrough) {
		ensureRunDaemonFn()
	}

	//nolint:contextcheck // The resident consumer lifecycle predates a context-aware process-table API.
	if cerr := ensureTriggerConsumerFn(paths.Root, idle, claimLease); cerr != nil {
		return fmt.Errorf("run %s is persisted but no consumer could be started to execute it: %w\n"+
			"Start one with `sparkwing runs consumer start`; the run is queued and will execute when it comes up",
			result.RunID, cerr)
	}

	if info, ok := orchestrator.ConsumerInfo(paths.Root); ok {
		result.ConsumerPID = info.PID
		if !info.Started.IsZero() {
			result.ConsumerStarted = info.Started.Format(time.RFC3339)
		}
	}
	return emitSubmitResult(result, format)
}

// resolveDetachedOutput names --sw-output rather than the -o/--output the
// shared resolver assumes, which a detached launch does not have.
func resolveDetachedOutput(v string) (string, error) {
	switch v {
	case "", "pretty", "json", "plain":
	default:
		return "", fmt.Errorf("%s: --sw-output must be one of pretty|json|plain, got %q", detachedPath, v)
	}
	return resolveTTYAwareOutput(v, detachedPath)
}

func parseConsumerDuration(flag, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%s %q: expected a duration such as 5m or 90s", flag, value)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s %q: must not be negative", flag, value)
	}
	return d, nil
}

type submission struct {
	Pipeline string
	Args     map[string]string
	RepoDir  string
	Ref      string
	// Priority is the unresolved --sw-priority value. It rides on the
	// trigger row, never in Args: it shapes where the run sits in the
	// admission queue, not what the pipeline does, so two submissions that
	// differ only here are the same intent and an idempotency key answers
	// the first one. front/back stay unresolved so the consumer's child
	// measures the queue it will actually join.
	Priority       string
	IdempotencyKey string
	RequestID      string

	// safety: empty keeps the source `run --sw-detached` writes; only a caller
	// with its own name for the launch sets this.
	Source string

	ScheduleID string

	PipelineRef string

	// safety: a locked cron schedule pins its own binary, and the consumer
	// execs this file instead of compiling the checkout.
	PinnedBinary string
	PinnedDigest string

	// safety: weighed against what the run will execute, which is the ref
	// worktree when the submission names a ref and the pinned binary when it
	// carries one, so a risk either declares cannot be queued past it. Every
	// caller sets one.
	Gate func(repoDir, pinnedBinary string) error
}

func persistSubmission(ctx context.Context, st *store.Store, paths orchestrator.Paths, sub submission) (submitResult, error) {
	if sub.Gate == nil {
		return submitResult{}, errors.New("persist submission: the submission carries no risk gate")
	}
	if sub.Ref != "" && sub.PipelineRef != "" {
		return submitResult{}, errors.New(
			"--sw-ref and --sw-pipeline-ref both name the tree the pipeline compiles from; pass one")
	}
	var rev orchestrator.Commit
	if sub.Ref != "" {
		resolved, rerr := orchestrator.ResolveRefCommit(ctx, sub.RepoDir, sub.Ref, slog.Default())
		if rerr != nil {
			return submitResult{}, fmt.Errorf("--sw-ref %s: %w", sub.Ref, rerr)
		}
		rev = resolved
	}
	var pipelineRev orchestrator.Commit
	if sub.PipelineRef != "" {
		resolved, rerr := orchestrator.ResolveRefCommit(ctx, sub.RepoDir, sub.PipelineRef, slog.Default())
		if rerr != nil {
			return submitResult{}, fmt.Errorf("--sw-pipeline-ref %s: %w", sub.PipelineRef, rerr)
		}
		pipelineRev = resolved
	}
	if existing, err := findExistingSubmission(ctx, st, sub.Pipeline, sub.IdempotencyKey); err != nil {
		return submitResult{}, err
	} else if existing != nil {
		return existingSubmissionResult(ctx, st, paths, existing, sub, rev, pipelineRev)
	}

	runID := orchestrator.NewLocalRunID()
	repoDir := sub.RepoDir
	var worktree string
	gateDir := repoDir
	if treeRev, flag := refTreeFor(sub, rev, pipelineRev); treeRev != "" {
		hold, held, herr := orchestrator.HoldRefWorktree(paths, runID)
		if herr != nil {
			return submitResult{}, fmt.Errorf("%s: could not hold a worktree for this run: %w", flag, herr)
		}
		if !held {
			return submitResult{}, fmt.Errorf("%s: another process already holds a worktree for run %s", flag, runID)
		}
		defer func() {
			if rerr := orchestrator.ReleaseRefWorktree(hold); rerr != nil {
				slog.Default().Warn("release worktree hold", "run_id", runID, "error", rerr)
			}
		}()

		built, werr := orchestrator.CreateRefWorktree(ctx, paths, sub.RepoDir, treeRev, runID, slog.Default())
		if werr != nil {
			return submitResult{}, fmt.Errorf("%s: %w", flag, werr)
		}
		worktree = built
		gateDir = built
		if pipelineRev == "" {
			repoDir = built
		}
	}
	if err := sub.Gate(gateDir, sub.PinnedBinary); err != nil {
		discardRefWorktree(ctx, paths, worktree)
		return submitResult{}, err
	}
	if err := orchestrator.CaptureSubmissionEnvironment(paths.Root, runID, os.Environ(), slog.Default()); err != nil {
		discardRefWorktree(ctx, paths, worktree)
		return submitResult{}, fmt.Errorf("capture submission environment: %w", err)
	}
	triggerEnv := map[string]string{
		orchestrator.SubmitRepoDirKey:                 repoDir,
		orchestrator.SubmissionEnvironmentCapturedKey: "1",
	}
	if rev != "" {
		triggerEnv[orchestrator.RefWorktreeRevKey] = string(rev)
	}
	if pipelineRev != "" {
		triggerEnv[orchestrator.PipelineRevKey] = string(pipelineRev)
		triggerEnv[orchestrator.PipelineDirKey] = worktree
	}
	if sub.Priority != "" {
		triggerEnv[orchestrator.SubmitPriorityKey] = sub.Priority
	}
	if sub.RequestID != "" {
		triggerEnv[SubmitRequestIDKey] = sub.RequestID
	}
	if sub.ScheduleID != "" {
		triggerEnv[crons.ScheduleEnvKey] = sub.ScheduleID
	}
	if sub.PinnedBinary != "" {
		triggerEnv[crons.PinnedBinaryEnvKey] = sub.PinnedBinary
		triggerEnv[crons.PinnedDigestEnvKey] = sub.PinnedDigest
	}
	var userName string
	if u, uerr := user.Current(); uerr == nil {
		userName = u.Username
	}
	branch, sha, repoSlug, repoURL := submitGitContext(repoDir)
	now := time.Now()

	trigger := store.Trigger{
		ID:             runID,
		Pipeline:       sub.Pipeline,
		Args:           sub.Args,
		TriggerSource:  submissionSource(sub),
		TriggerUser:    userName,
		TriggerEnv:     triggerEnv,
		GitBranch:      branch,
		GitSHA:         sha,
		Repo:           repoSlug,
		RepoURL:        repoURL,
		CreatedAt:      now,
		IdempotencyKey: sub.IdempotencyKey,
	}
	if owner, name := sparkwingOwnerRepo(repoSlug); owner != "" {
		trigger.GithubOwner, trigger.GithubRepo = owner, name
	}

	if err := st.CreateTrigger(ctx, trigger); err != nil {
		if derr := orchestrator.DiscardSubmissionEnvironment(paths.Root, runID); derr != nil {
			slog.Default().Warn("discard submission environment", "run_id", runID, "error", derr)
		}
		discardRefWorktree(ctx, paths, worktree)

		if errors.Is(err, store.ErrDuplicateIdempotencyKey) {
			existing, ferr := st.FindTriggerByIdempotencyKey(ctx, sub.Pipeline, sub.IdempotencyKey)
			if ferr == nil {
				return existingSubmissionResult(ctx, st, paths, existing, sub, rev, pipelineRev)
			}
			return submitResult{}, fmt.Errorf("persist trigger: %w", err)
		}
		return submitResult{}, fmt.Errorf("persist trigger: %w", err)
	}

	if err := st.CreateRun(ctx, store.Run{
		ID:            runID,
		Pipeline:      sub.Pipeline,
		Status:        "pending",
		TriggerSource: trigger.TriggerSource,
		GitBranch:     branch,
		GitSHA:        sha,
		Args:          sub.Args,
		DeclaredRepo:  repoSlug,
		RepoURL:       repoURL,
		GithubOwner:   trigger.GithubOwner,
		GithubRepo:    trigger.GithubRepo,
		CreatedAt:     now,
		StartedAt:     now,
	}); err != nil {
		discardRefWorktree(ctx, paths, worktree)
		return submitResult{}, fmt.Errorf("persist run: %w", err)
	}

	return submitResult{
		RunHandle:      orchestrator.NewRunHandle(runID, sub.Pipeline, orchestrator.EnsureRunLogDir(paths, runID), "pending"),
		IdempotencyKey: sub.IdempotencyKey,
		RequestID:      sub.RequestID,
	}, nil
}

// safety: a named source passes through verbatim; pipelines branch on that exact word.
func submissionSource(sub submission) string {
	if sub.Source != "" {
		return sub.Source
	}
	return triggerSource(submitTriggerSourcePrefix)
}

func discardRefWorktree(ctx context.Context, paths orchestrator.Paths, dir string) {
	if dir == "" {
		return
	}
	if err := orchestrator.RemoveRefWorktree(ctx, paths, dir, slog.Default()); err != nil {
		slog.Default().Warn("submission left a ref worktree behind", "dir", dir, "error", err)
	}
}

func checkRefMatchesOriginal(existing *store.Trigger, sub submission, rev orchestrator.Commit) error {
	// safety: comparing resolved commits rather than ref names catches a branch
	// that moved between the two submissions.
	original := strings.TrimSpace(existing.TriggerEnv[orchestrator.RefWorktreeRevKey])
	if original == string(rev) {
		return nil
	}
	return fmt.Errorf(
		"run --sw-detached: idempotency key %q already ran pipeline %q against a different tree, "+
			"so answering this submission with that run would report a verdict for code it never executed.\n"+
			"  original: %s\n"+
			"  this one: %s\n"+
			"Original run: %s\n"+
			"Use a new key for the new tree, or resubmit against the original commit",
		sub.IdempotencyKey, existing.Pipeline,
		describeRefTree(original), describeRefTree(string(rev)), existing.ID)
}

func refTreeFor(sub submission, rev, pipelineRev orchestrator.Commit) (orchestrator.Commit, string) {
	if pipelineRev != "" {
		return pipelineRev, "--sw-pipeline-ref " + sub.PipelineRef
	}
	if rev != "" {
		return rev, "--sw-ref " + sub.Ref
	}
	return "", ""
}

func checkPipelineRefMatchesOriginal(existing *store.Trigger, sub submission, pipelineRev orchestrator.Commit) error {
	original := strings.TrimSpace(existing.TriggerEnv[orchestrator.PipelineRevKey])
	if original == string(pipelineRev) {
		return nil
	}
	return fmt.Errorf(
		"%s: idempotency key %q already ran pipeline %q compiled from a different tree "+
			"(%s; requested %s). Original run: %s. Use a new key for the new pipeline source",
		detachedPath, sub.IdempotencyKey, existing.Pipeline,
		describeRefTree(original), describeRefTree(string(pipelineRev)), existing.ID)
}

func describeRefTree(rev string) string {
	if rev == "" {
		return "the submitting checkout"
	}
	return "commit " + rev
}

func findExistingSubmission(ctx context.Context, st *store.Store, pipeline, key string) (*store.Trigger, error) {
	if key == "" {
		return nil, nil
	}
	existing, err := st.FindTriggerByIdempotencyKey(ctx, pipeline, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look up idempotency key %q: %w", key, err)
	}
	return existing, nil
}

func existingSubmissionResult(
	ctx context.Context, st *store.Store, paths orchestrator.Paths,
	existing *store.Trigger, sub submission, rev, pipelineRev orchestrator.Commit,
) (submitResult, error) {
	if err := checkRefMatchesOriginal(existing, sub, rev); err != nil {
		return submitResult{}, err
	}
	if err := checkPipelineRefMatchesOriginal(existing, sub, pipelineRev); err != nil {
		return submitResult{}, err
	}
	if diff := describeArgsMismatch(existing.Args, sub.Args); diff != "" {
		return submitResult{}, fmt.Errorf(
			"run --sw-detached: idempotency key %q was already used for pipeline %q with different arguments, "+
				"so this is a different request rather than a retry of that one.\n"+
				"  %s\n"+
				"Original run: %s\n"+
				"Use a new key for the new arguments, or resubmit with the original ones",
			sub.IdempotencyKey, existing.Pipeline, diff, existing.ID)
	}

	status := ""
	if run, err := st.GetRun(ctx, existing.ID); err == nil && run != nil {
		status = run.Status
	}

	logPath := existingRunLogDir(paths, existing.ID)
	return submitResult{
		RunHandle:        orchestrator.NewRunHandle(existing.ID, existing.Pipeline, logPath, status),
		AlreadySubmitted: true,
		IdempotencyKey:   existing.IdempotencyKey,
		RequestID:        sub.RequestID,
	}, nil
}

func existingRunLogDir(paths orchestrator.Paths, runID string) string {
	dir, err := filepath.Abs(paths.RunDir(runID))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// describeArgsMismatch compares only the pipeline's arguments. --sw-priority is
// absent from that map: a key names one intent, and resubmitting the
// same work in a hurry is that same intent, so a repeat that differs only in
// queue position answers with the original run rather than being refused.
func describeArgsMismatch(original, incoming map[string]string) string {
	if len(original) == len(incoming) {
		same := true
		for k, v := range original {
			if incoming[k] != v {
				same = false
				break
			}
		}
		if same {
			return ""
		}
	}
	return fmt.Sprintf("original arguments %s, this submission %s",
		renderArgs(original), renderArgs(incoming))
}

func renderArgs(args map[string]string) string {
	if len(args) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("--%s=%s", k, args[k]))
	}
	return strings.Join(parts, " ")
}

func submitPaths(home string) (orchestrator.Paths, error) {
	if home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return orchestrator.Paths{}, fmt.Errorf("resolve --home %s: %w", home, err)
		}
		return orchestrator.PathsAt(abs), nil
	}
	return orchestrator.DefaultPaths()
}

func resolveSubmitRepo(ctx context.Context, pipeline, changeDir, pipelineRef string) (string, []sparkwing.DescribePipeline, error) {
	start := changeDir
	if start == "" {
		start = mustGetwd()
	}
	if pipelineRef != "" {
		dir, err := gitOutput(ctx, start, nil, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", nil, err
		}
		dir = strings.TrimSpace(dir)
		if info, err := os.Stat(filepath.Join(dir, ".sparkwing", "sparkwing.yaml")); err != nil || info.IsDir() {
			return "", nil, fmt.Errorf("execution checkout %s requires .sparkwing/sparkwing.yaml", dir)
		}
		return dir, nil, nil
	}
	if dir, declared, ok := localRepoDeclaring(start, pipeline); ok {
		return dir, declared, nil
	}

	path, err := repos.ResolveRepoForPipelineCached(pipeline)
	if err == nil {
		return path, nil, nil
	}
	if errors.Is(err, repos.ErrNotFound) {
		return "", nil, fmt.Errorf(
			"run --sw-detached: no project here or in the repo registry declares a pipeline named %q.\n"+
				"Run it from the checkout that defines it, pass -C <path> to point at that checkout, "+
				"or register it with `sparkwing configure xrepo add <path>`.\n"+
				"A registered checkout whose pipeline binary has never been built is not searched; "+
				"run `sparkwing pipeline list` there once first", pipeline)
	}
	return "", nil, fmt.Errorf("run --sw-detached: resolve %q: %w", pipeline, err)
}

func localRepoDeclaring(start, pipeline string) (string, []sparkwing.DescribePipeline, bool) {
	sparkwingDir, err := findSparkwingDirFrom(start)
	if err != nil {
		return "", nil, false
	}
	repoDir := filepath.Dir(sparkwingDir)
	declared, err := repos.DescribeRepo(repoDir)
	if err != nil {
		return "", nil, false
	}
	for _, s := range declared {
		if s.Name == pipeline {
			return repoDir, declared, true
		}
	}
	return "", nil, false
}

// foregroundOnlyReasons says, per flag, why a detached run cannot honor it.
// Each is refused rather than ignored: a run that quietly dropped one would
// report a verdict for work the operator did not ask for.
func foregroundOnlyReasons(wf runFlags) []struct {
	name   string
	set    bool
	reason string
} {
	return []struct {
		name   string
		set    bool
		reason string
	}{
		{"--sw-index", wf.index != "", "an index binding names a file in your filesystem that sparkwing neither creates nor can " +
			"reproduce, so a detached run would read whatever that path holds when it starts, or nothing. " +
			"Run it in the foreground with `sparkwing run --sw-index`"},
		{"--sw-dry-run", wf.dryRun, "a dry run finishes in seconds and reports to your terminal; run it in the foreground with `sparkwing run --sw-dry-run`"},

		{"--profile", wf.profile != "", "the resident consumer executes against this home's local store, " +
			"so a profile's backends would not receive the run; run profile-backed runs in the foreground"},

		{"--sw-start-at", wf.startAt != "", "step-window selection is not carried on the trigger yet; run it in the foreground"},
		{"--sw-stop-at", wf.stopAt != "", "step-window selection is not carried on the trigger yet; run it in the foreground"},
		{"--sw-only", wf.only != "", "job filtering is not carried on the trigger yet; run it in the foreground"},
		{"--sw-no-cache", wf.noCache, "cache-read suppression is not carried on the trigger yet; run it in the foreground"},
		{"--sw-mode", wf.mode != "", "execution mode is not carried on the trigger yet; run it in the foreground"},
		{"--sw-workers", wf.workers > 0, "worker capping is not carried on the trigger yet; run it in the foreground"},
		{"--sw-allow", len(wf.allow) > 0, "risk authorization is not carried on the trigger yet; run it in the foreground"},
		{"--sw-local-only", wf.localOnly, "backend overrides are not carried on the trigger yet; run it in the foreground"},
		{"--sw-secrets", wf.secrets != "", "secret-profile selection is not carried on the trigger yet; run it in the foreground"},
		{"--sw-no-update", wf.noUpdate, "the consumer compiles the run, and the flag is not carried on the trigger; " +
			"set SPARKWING_NO_UPDATE=1 in this shell instead, which the submission environment snapshot carries, " +
			"or run it in the foreground"},
		{"--sw-fleet", wf.fleet, "enrolled helpers execute under the lifetime of the coordinating foreground process, " +
			"which a detached run does not have; run it in the foreground with `sparkwing run --sw-fleet`"},
	}
}

// safety: the consumer executes a queued run with no operator attached, and
// the trigger carries no allow, so a declared risk has to refuse before the
// run is persisted or it reaches the consumer authorized by nothing.
type riskGate struct {
	Surface  string
	Pipeline string
	Flags    runFlags

	// safety: a checkout whose declarations the caller already read, so the
	// gate weighs that build instead of making a second one.
	SubmitDir string
	Declared  []sparkwing.DescribePipeline
}

func (g riskGate) check(execDir, pinnedBinary string) error {
	sparkwingDir := filepath.Join(execDir, ".sparkwing")
	declared := g.Declared
	switch {
	case pinnedBinary != "":
		raw, err := runDescribeBinary(context.Background(), sparkwingDir, pinnedBinary)
		if err != nil {
			return fmt.Errorf("%s: read what %s pins: %w", g.Surface, g.Pipeline, err)
		}
		if err := json.Unmarshal(raw, &declared); err != nil {
			return fmt.Errorf("%s: read what %s pins: %w", g.Surface, g.Pipeline, err)
		}
	case declared == nil || execDir != g.SubmitDir:
		var err error
		declared, err = repos.DescribeRepo(execDir)
		if err != nil {
			return fmt.Errorf("%s: read what %s declares: %w", g.Surface, g.Pipeline, err)
		}
	}
	if err := enforceRiskGate(g.Pipeline, risksIn(declared, g.Pipeline), g.Flags); err != nil {
		return fmt.Errorf("%s: %w\n"+
			"A queued run carries neither an allow nor a dry run, so a pipeline that declares a risk "+
			"runs in the foreground",
			g.Surface, err)
	}
	return nil
}

func refuseForegroundOnlyFlags(wf runFlags) error {
	for _, f := range foregroundOnlyReasons(wf) {
		if !f.set {
			continue
		}
		return fmt.Errorf("%s: %s cannot be honored by a detached run: %s", detachedPath, f.name, f.reason)
	}
	return nil
}

func refuseDetachedOnlyFlags(wf runFlags) error {
	for _, f := range wf.detachedOnlyFlags() {
		if f.value == "" {
			continue
		}
		return fmt.Errorf("run: %s is read only by a detached launch; add --sw-detached, or drop the flag", f.name)
	}
	return nil
}

func submitGitContext(repoDir string) (branch, sha, repoSlug, repoURL string) {
	return gitContextIn(repoDir)
}

func sparkwingOwnerRepo(slug string) (owner, name string) {
	i := strings.IndexByte(slug, '/')
	if i <= 0 || i == len(slug)-1 {
		return "", ""
	}
	return slug[:i], slug[i+1:]
}

func emitSubmitResult(r submitResult, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(r)
	case "plain":
		fmt.Fprintln(os.Stdout, r.RunID)
		return nil
	default:
		if r.AlreadySubmitted {
			status := r.Status
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(os.Stdout, "run %s already submitted (%s), status %s\n",
				r.RunID, r.Pipeline, status)
		} else {
			fmt.Fprintf(os.Stdout, "run %s submitted (%s)\n", r.RunID, r.Pipeline)
		}
		if r.LogPath != "" {
			fmt.Fprintf(os.Stdout, "  logs:   %s\n", r.LogPath)
		}
		if r.ConsumerPID != 0 {
			fmt.Fprintf(os.Stdout, "  runner: consumer pid %d (started %s)\n",
				r.ConsumerPID, r.ConsumerStarted)
		}
		fmt.Fprintf(os.Stdout, "  follow: sparkwing runs logs --run %s --follow\n", r.RunID)
		fmt.Fprintf(os.Stdout, "  cancel: sparkwing runs cancel --run %s\n", r.RunID)
		if r.AlreadySubmitted && isTerminalRunStatus(r.Status) {
			fmt.Fprintf(os.Stdout,
				"  note:   this run already finished (%s); nothing new was queued. "+
					"Use a different --sw-idempotency-key to run it again.\n", r.Status)
		}
		return nil
	}
}
