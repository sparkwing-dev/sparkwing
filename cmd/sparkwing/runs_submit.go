package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const SubmitRequestIDKey = "_SPARKWING_SUBMIT_REQUEST_ID"

const submitTriggerSourcePrefix = "runs-submit"

type submitResult struct {
	orchestrator.RunHandle

	AlreadySubmitted bool `json:"already_submitted,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
	RequestID      string `json:"request_id,omitempty"`

	ConsumerPID     int    `json:"consumer_pid,omitempty"`
	ConsumerStarted string `json:"consumer_started,omitempty"`
}

func runRunsSubmit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsSubmit.Path, flag.ContinueOnError)
	idempotencyKey := fs.String("idempotency-key", "",
		"deduplication token: a repeat submission carrying this key returns the original run instead of starting a second one")
	requestID := fs.String("request-id", "",
		"tracing identifier recorded on the run; never affects deduplication")
	home := fs.String("home", "", "sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	changeDir := fs.StringP("cd", "C", "", "resolve the pipeline from this directory instead of the current one")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")

	idle := fs.Duration("consumer-idle", 0,
		"if this starts a consumer: how long it stays alive with no work (default 5m)")
	claimLease := fs.Duration("consumer-claim-lease", 0,
		"if this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m)")

	fs.SetInterspersed(false)

	own, forPipeline := splitAtSeparator(args)
	if err := refuseUndetachableFlags(own); err != nil {
		return err
	}
	if err := parseAndCheck(cmdJobsSubmit, fs, own); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("%s: a pipeline name is required (see `sparkwing pipeline list`)", cmdJobsSubmit.Path)
	}
	pipeline := rest[0]
	if err := refuseMisplacedSubmitFlags(rest[1:]); err != nil {
		return err
	}
	passthrough := append(append([]string{}, rest[1:]...), forPipeline...)
	format, err := resolveTTYAwareOutput(*outFmt, cmdJobsSubmit.Path)
	if err != nil {
		return err
	}

	repoDir, err := resolveSubmitRepo(pipeline, *changeDir)
	if err != nil {
		return err
	}

	paths, err := submitPaths(*home)
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return fmt.Errorf("ensure %s: %w", paths.Root, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open %s: %w", paths.StateDB(), err)
	}
	defer func() { _ = st.Close() }()

	result, err := persistSubmission(ctx, st, paths, submission{
		Pipeline:       pipeline,
		Args:           collectPipelineArgs(passthrough),
		RepoDir:        repoDir,
		IdempotencyKey: strings.TrimSpace(*idempotencyKey),
		RequestID:      strings.TrimSpace(*requestID),
	})
	if err != nil {
		return err
	}

	if cerr := ensureTriggerConsumer(paths.Root, *idle, *claimLease); cerr != nil {
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

type submission struct {
	Pipeline       string
	Args           map[string]string
	RepoDir        string
	IdempotencyKey string
	RequestID      string
}

func persistSubmission(ctx context.Context, st *store.Store, paths orchestrator.Paths, sub submission) (submitResult, error) {
	if existing, err := findExistingSubmission(ctx, st, sub.Pipeline, sub.IdempotencyKey); err != nil {
		return submitResult{}, err
	} else if existing != nil {
		return existingSubmissionResult(ctx, st, paths, existing, sub)
	}

	runID := orchestrator.NewLocalRunID()
	if err := orchestrator.CaptureSubmissionEnvironment(paths.Root, runID, os.Environ()); err != nil {
		return submitResult{}, fmt.Errorf("capture submission environment: %w", err)
	}
	triggerEnv := map[string]string{
		orchestrator.SubmitRepoDirKey:                 sub.RepoDir,
		orchestrator.SubmissionEnvironmentCapturedKey: "1",
	}
	if sub.RequestID != "" {
		triggerEnv[SubmitRequestIDKey] = sub.RequestID
	}
	var userName string
	if u, uerr := user.Current(); uerr == nil {
		userName = u.Username
	}
	branch, sha, repoSlug, repoURL := submitGitContext(sub.RepoDir)
	now := time.Now()

	trigger := store.Trigger{
		ID:             runID,
		Pipeline:       sub.Pipeline,
		Args:           sub.Args,
		TriggerSource:  triggerSource(submitTriggerSourcePrefix),
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
		_ = orchestrator.DiscardSubmissionEnvironment(paths.Root, runID)

		if errors.Is(err, store.ErrDuplicateIdempotencyKey) {
			existing, ferr := st.FindTriggerByIdempotencyKey(ctx, sub.Pipeline, sub.IdempotencyKey)
			if ferr == nil {
				return existingSubmissionResult(ctx, st, paths, existing, sub)
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
		Repo:          repoSlug,
		RepoURL:       repoURL,
		GithubOwner:   trigger.GithubOwner,
		GithubRepo:    trigger.GithubRepo,
		CreatedAt:     now,
		StartedAt:     now,
	}); err != nil {
		return submitResult{}, fmt.Errorf("persist run: %w", err)
	}

	return submitResult{
		RunHandle:      orchestrator.NewRunHandle(runID, sub.Pipeline, orchestrator.EnsureRunLogDir(paths, runID), "pending"),
		IdempotencyKey: sub.IdempotencyKey,
		RequestID:      sub.RequestID,
	}, nil
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
	existing *store.Trigger, sub submission,
) (submitResult, error) {
	if diff := describeArgsMismatch(existing.Args, sub.Args); diff != "" {
		return submitResult{}, fmt.Errorf(
			"runs submit: idempotency key %q was already used for pipeline %q with different arguments, "+
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

func resolveSubmitRepo(pipeline, changeDir string) (string, error) {
	start := changeDir
	if start == "" {
		start = mustGetwd()
	}
	if dir, ok := localRepoDeclaring(start, pipeline); ok {
		return dir, nil
	}

	path, err := repos.ResolveRepoForPipelineCached(pipeline)
	if err == nil {
		return path, nil
	}
	if errors.Is(err, repos.ErrNotFound) {
		return "", fmt.Errorf(
			"runs submit: no project here or in the repo registry declares a pipeline named %q.\n"+
				"Run it from the checkout that defines it, pass -C <path> to point at that checkout, "+
				"or register it with `sparkwing configure xrepo add <path>`.\n"+
				"A registered checkout whose pipeline binary has never been built is not searched; "+
				"run `sparkwing pipeline list` there once first", pipeline)
	}
	return "", fmt.Errorf("runs submit: resolve %q: %w", pipeline, err)
}

func localRepoDeclaring(start, pipeline string) (string, bool) {
	sparkwingDir, err := findSparkwingDirFrom(start)
	if err != nil {
		return "", false
	}
	repoDir := filepath.Dir(sparkwingDir)
	names, err := repos.PipelineNamesForRepo(repoDir)
	if err != nil {
		return "", false
	}
	for _, n := range names {
		if n == pipeline {
			return repoDir, true
		}
	}
	return "", false
}

var undetachableFlags = map[string]string{
	"--sw-index": "an index binding is a live path this process holds open for the run; " +
		"a detached run outlives the submitting process. Run it in the foreground with `sparkwing run --sw-index`",
	"--sw-ref": "a --sw-ref worktree is created and removed around a foreground run; " +
		"nothing would clean it up after a detached one. Check the ref out and submit from that checkout",
	"--sw-dry-run": "a dry run finishes in seconds and reports to your terminal; submit it with `sparkwing run --sw-dry-run`",

	"--profile": "the resident consumer executes against this home's local store, " +
		"so a profile's backends would not receive the run; run profile-backed runs in the foreground",

	"--sw-start-at":   "step-window selection is not carried on the trigger yet; run it in the foreground",
	"--sw-stop-at":    "step-window selection is not carried on the trigger yet; run it in the foreground",
	"--sw-only":       "job filtering is not carried on the trigger yet; run it in the foreground",
	"--sw-no-cache":   "cache-read suppression is not carried on the trigger yet; run it in the foreground",
	"--sw-mode":       "execution mode is not carried on the trigger yet; run it in the foreground",
	"--sw-workers":    "worker capping is not carried on the trigger yet; run it in the foreground",
	"--sw-allow":      "risk authorization is not carried on the trigger yet; run it in the foreground",
	"--sw-local-only": "backend overrides are not carried on the trigger yet; run it in the foreground",
	"--sw-secrets":    "secret-profile selection is not carried on the trigger yet; run it in the foreground",
}

var submitOwnedFlags = map[string]string{
	"--idempotency-key": "",
	"--request-id":      "",
	"--home":            "",
	"--consumer-idle":   "",
	"--cd":              "",
	"-C":                "",
	"--output":          "",
	"-o":                "",
}

func refuseMisplacedSubmitFlags(passthrough []string) error {
	for _, arg := range passthrough {
		name := arg
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if _, mine := submitOwnedFlags[name]; !mine {
			continue
		}
		return fmt.Errorf(
			"runs submit: %s is a flag of `runs submit`, but it appears after the pipeline name, "+
				"where every argument belongs to the pipeline.\n"+
				"Move it before the pipeline name:\n"+
				"  sparkwing runs submit %s <pipeline> [pipeline-flags...]\n"+
				"If the pipeline really declares its own %s, separate the two with `--`:\n"+
				"  sparkwing runs submit <pipeline> -- %s <pipeline-args>",
			name, name, name, name)
	}
	return nil
}

func splitAtSeparator(args []string) (own, forPipeline []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func refuseUndetachableFlags(passthrough []string) error {
	for _, arg := range passthrough {
		name := arg
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if reason, bad := undetachableFlags[name]; bad {
			return fmt.Errorf("runs submit: %s cannot be honored by a detached run: %s", name, reason)
		}
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
		enc.SetIndent("", "  ")
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
			fmt.Fprintf(os.Stdout, "  runner: consumer pid %d (started %s); the run uses ITS environment, not this shell's\n",
				r.ConsumerPID, r.ConsumerStarted)
		}
		fmt.Fprintf(os.Stdout, "  follow: sparkwing runs logs --run %s --follow\n", r.RunID)
		fmt.Fprintf(os.Stdout, "  cancel: sparkwing runs cancel --run %s\n", r.RunID)
		if r.AlreadySubmitted && isTerminalRunStatus(r.Status) {
			fmt.Fprintf(os.Stdout,
				"  note:   this run already finished (%s); nothing new was queued. "+
					"Use a different --idempotency-key to run it again.\n", r.Status)
		}
		return nil
	}
}
