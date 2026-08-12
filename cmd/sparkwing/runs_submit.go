// `sparkwing runs submit` -- hand a local run to the machine and walk
// away.
//
// `sparkwing run` executes in the caller's terminal: close it and the
// run dies with it. Submit inverts that. It writes the trigger and a
// pending run row, makes sure a resident consumer owns this home's
// queue, and prints the run id and log directory. From that moment the
// run belongs to the machine: the terminal can close, the ssh session
// can drop, and the run is still either recoverable or terminal, never
// missing.
//
// The ordering is the contract. Persist, then confirm ownership, then
// acknowledge -- never the reverse. A run id printed before the row is
// durable would be a receipt for nothing.
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

// SubmitRequestIDKey is the TriggerEnv entry carrying the caller's
// tracing identifier.
//
// It is not the deduplication key and must never be treated as one.
// A caller correlating its own logs with a run wants a fresh id on every
// attempt; a caller retrying an ambiguous submission wants the same
// idempotency key on every attempt so it reaches the original run. Fold
// the two together and one of the callers is wrong: either retries
// silently start extra runs, or a deliberate resubmission is swallowed
// as a duplicate.
const SubmitRequestIDKey = "_SPARKWING_SUBMIT_REQUEST_ID"

// submitTriggerSource tags submitted runs in `runs list` so a detached
// submission is distinguishable from a foreground `sparkwing run`.
const submitTriggerSourcePrefix = "runs-submit"

// submitResult is the acknowledgment, in the shape `-o json` emits.
type submitResult struct {
	RunID    string `json:"run_id"`
	Pipeline string `json:"pipeline"`
	// LogPath is the directory this run's node logs land in. Present
	// only when the directory exists -- the same rule the run_start
	// receipt follows, so a caller can trust the field or its absence
	// but never has to test whether a named directory is real.
	LogPath string `json:"log_path,omitempty"`
	// AlreadySubmitted is true when an idempotency key matched an
	// earlier submission and this call created nothing.
	AlreadySubmitted bool `json:"already_submitted,omitempty"`
	// Status is the matched run's current status on the duplicate path.
	// A duplicate acknowledgment that said only "already submitted"
	// would let a caller assume work is under way when the original may
	// have failed hours ago.
	Status         string `json:"status,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	// ConsumerPID and ConsumerStarted identify the process that will
	// execute the run. They are reported because that process supplies
	// the run's environment, and it is not this shell's -- see the
	// environment note in the command's help.
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
		"how long the resident consumer stays alive with no work (default 5m)")
	claimLease := fs.Duration("consumer-claim-lease", 0,
		"lease the consumer stamps on each claimed run, renewed while it executes (default 3m)")
	// Everything after the pipeline name belongs to the pipeline, so the
	// flag set stops at the first operand.
	fs.SetInterspersed(false)

	// Checked before parsing, on the raw arguments. After parsing, a
	// run-shaping flag typed before the pipeline name has already failed
	// as an unknown flag, and "unknown flag: --sw-dry-run" does not tell
	// the caller the one thing worth knowing: that this is a real flag
	// which a detached run cannot honor, and where to run it instead.
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

	// The run is durable now. Ownership is what turns durable into
	// running, so a failure here is reported with the run id rather than
	// swallowed: the row is real either way, and the caller needs to know
	// which half of the acknowledgment it got.
	if cerr := ensureTriggerConsumer(paths.Root, *idle, *claimLease); cerr != nil {
		return fmt.Errorf("run %s is persisted but no consumer could be started to execute it: %w\n"+
			"Start one with `sparkwing runs consumer start`; the run is queued and will execute when it comes up",
			result.RunID, cerr)
	}
	// Named in the acknowledgment because this process, not this shell,
	// supplies the run's environment.
	if info, ok := orchestrator.ConsumerInfo(paths.Root); ok {
		result.ConsumerPID = info.PID
		if !info.Started.IsZero() {
			result.ConsumerStarted = info.Started.Format(time.RFC3339)
		}
	}
	return emitSubmitResult(result, format)
}

// submission is one caller's request, already validated.
type submission struct {
	Pipeline       string
	Args           map[string]string
	RepoDir        string
	IdempotencyKey string
	RequestID      string
}

// persistSubmission writes the trigger and its pending run, or resolves
// the submission to a run an earlier call already created.
//
// Both rows go in before the function returns and both carry the same
// id, mirroring the controller's trigger handler: the trigger is the
// queue entry, the pending run is what `runs status` and the dashboard
// can already see while the work waits. Creating only the trigger would
// leave an acknowledged run id that looks unknown to every read path
// until a consumer picked it up.
func persistSubmission(ctx context.Context, st *store.Store, paths orchestrator.Paths, sub submission) (submitResult, error) {
	if existing, err := findExistingSubmission(ctx, st, sub.Pipeline, sub.IdempotencyKey); err != nil {
		return submitResult{}, err
	} else if existing != nil {
		return existingSubmissionResult(ctx, st, paths, existing, sub)
	}

	runID := orchestrator.NewLocalRunID()
	triggerEnv := map[string]string{orchestrator.SubmitRepoDirKey: sub.RepoDir}
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
		// Losing the unique-index race means another submission carrying
		// this key won. That is the idempotent outcome, not a failure:
		// resolve to the winner and report it as already submitted.
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
		RunID:          runID,
		Pipeline:       sub.Pipeline,
		LogPath:        orchestrator.EnsureRunLogDir(paths, runID),
		IdempotencyKey: sub.IdempotencyKey,
		RequestID:      sub.RequestID,
	}, nil
}

// findExistingSubmission returns the trigger an earlier submission
// created under this pipeline and key, or nil when the pair is unused or
// no key was supplied.
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

// existingSubmissionResult renders the acknowledgment for a duplicate.
// The run id is the original one, which is the whole point: a caller
// retrying after a dropped connection reaches the run it already has
// rather than starting a second one.
//
// Two things make that answer honest rather than merely convenient.
//
// A key stands for one intent, so a resubmission whose arguments differ
// from the original's is not a retry of that intent -- it is a different
// request wearing the same name, and answering it with the original run
// would silently drop it. That is refused instead.
//
// And the original may have finished long ago, possibly badly. A bare
// "already submitted" invites the caller to believe work is under way
// when it may be holding a corpse, so the acknowledgment carries the
// run's current status.
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
	// The log directory is only ensured for a run this call created. On
	// the duplicate path the run is somebody else's and may be long
	// finished or already pruned, so the path is reported when it is
	// really there and omitted otherwise -- never conjured by creating
	// an empty directory for a run that has none.
	logPath := existingRunLogDir(paths, existing.ID)
	return submitResult{
		RunID:            existing.ID,
		Pipeline:         existing.Pipeline,
		LogPath:          logPath,
		AlreadySubmitted: true,
		Status:           status,
		IdempotencyKey:   existing.IdempotencyKey,
		RequestID:        sub.RequestID,
	}, nil
}

// existingRunLogDir reports an already-existing run's log directory, or
// "" when there is none. Unlike EnsureRunLogDir it never creates one.
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

// describeArgsMismatch returns a one-line description of how two
// argument sets differ, or "" when they match.
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

// renderArgs prints an argument map in a stable order so two renderings
// of the same arguments read identically.
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

// submitPaths resolves the home this submission writes to.
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

// resolveSubmitRepo picks the checkout whose .sparkwing/ defines
// pipeline, and proves it declares that pipeline before anything is
// written.
//
// The checkout the caller is standing in wins over the repo registry.
// Two checkouts of one project both declare the same pipeline names, and
// the registry has no way to know which one the person meant; the
// working directory does. Falling back to the registry afterwards is
// what lets a submission name a pipeline from anywhere on the machine.
func resolveSubmitRepo(pipeline, changeDir string) (string, error) {
	start := changeDir
	if start == "" {
		start = mustGetwd()
	}
	if dir, ok := localRepoDeclaring(start, pipeline); ok {
		return dir, nil
	}
	// The cached resolver, deliberately. Its compiling sibling would
	// build every registered checkout on the machine to answer one
	// question, which is not a price an interactive command may charge --
	// and it is the resolver the consumer itself uses, so a submission
	// this accepts is one the consumer can locate.
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

// localRepoDeclaring reports the checkout at or above start whose
// compiled .sparkwing/ registers pipeline. Compiling here is deliberate:
// a submission that names a pipeline the local project does not have
// must fail in the caller's terminal, where the person can read the
// error, rather than land in the queue and fail later in a log file.
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

// undetachableFlags are the `sparkwing run` flags a submission refuses,
// each with the reason it gives.
//
// They are refused rather than ignored because every one of them changes
// what the run does, and accepting a flag while running something else
// is the worst outcome available: a caller that asked for a dry run and
// got a real one has no way to tell from the acknowledgment.
//
// Two different kinds live here, and the distinction matters for
// anyone deciding what to do about them.
//
// CAN NEVER DETACH -- the flag's meaning is tied to the submitting
// process, so no amount of plumbing makes it work: --sw-index (a live
// path this process holds open), --sw-ref (a worktree created and
// removed around a foreground run), --sw-dry-run (a seconds-long
// inspection whose whole output is the terminal it reports to), and
// --profile (see the note at the refusal site).
//
// NOT CARRIED YET -- the flag is perfectly meaningful for a detached
// run, but the trigger does not carry it and TriggerEnv is not exported
// into the dispatched child's environment, so accepting it today would
// silently ignore it. These become supportable by threading them onto
// the trigger, and are refused only until then.
var undetachableFlags = map[string]string{
	// Can never detach.
	"--sw-index": "an index binding is a live path this process holds open for the run; " +
		"a detached run outlives the submitting process. Run it in the foreground with `sparkwing run --sw-index`",
	"--sw-ref": "a --sw-ref worktree is created and removed around a foreground run; " +
		"nothing would clean it up after a detached one. Check the ref out and submit from that checkout",
	"--sw-dry-run": "a dry run finishes in seconds and reports to your terminal; submit it with `sparkwing run --sw-dry-run`",
	// A profile-backed run writes its state, logs, and cache to that
	// profile's backends. The resident consumer opens this home's local
	// store and nothing else, so accepting --profile would record the run
	// in the wrong place -- and the log_path this command acknowledges,
	// which names a directory under this home, would be a lie about where
	// the run's logs went.
	"--profile": "the resident consumer executes against this home's local store, " +
		"so a profile's backends would not receive the run; run profile-backed runs in the foreground",
	// Not carried yet.
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

// submitOwnedFlags are the flags `runs submit` reads itself. They must
// precede the pipeline name, because everything after it is the
// pipeline's own argument list.
//
// The list exists so a misplaced one is refused rather than absorbed.
// Parsing stops at the pipeline name, so `runs submit deploy
// --idempotency-key k` would otherwise hand the key to the pipeline as
// an argument and run with no deduplication at all -- a caller
// believing its retry was safe when it was not. That is the same
// silent-misinterpretation failure the undetachable-flag refusal
// exists to prevent, and it deserves the same treatment.
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

// refuseMisplacedSubmitFlags rejects a submit-owned flag that appears
// after the pipeline name.
//
// A pipeline that genuinely declares a flag by one of these names is not
// stuck: `--` ends this command's arguments, and everything after it
// reaches the pipeline untouched.
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
				"  sparkwing runs submit <pipeline> -- %s ...",
			name, name, name, name)
	}
	return nil
}

// splitAtSeparator divides args at the first bare `--`. Arguments before
// it are this command's to parse; arguments after it are the pipeline's,
// whatever they are named.
func splitAtSeparator(args []string) (own, forPipeline []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// refuseUndetachableFlags rejects a submission naming a flag detached
// execution cannot honor, before anything is persisted. It is given only
// the arguments before a `--` separator: past that point the names
// belong to the pipeline, and a pipeline's own `--profile` is not this
// command's to refuse.
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

// submitGitContext reads repoDir's git identity for the trigger's
// provenance fields. Every field is best-effort: a project without a git
// remote still submits, it just records less.
func submitGitContext(repoDir string) (branch, sha, repoSlug, repoURL string) {
	return gitContextIn(repoDir)
}

// sparkwingOwnerRepo splits "owner/name"; empty when the slug is not in
// that shape.
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
			// Named because it is load-bearing: this process supplies the
			// run's environment, and it is not this shell.
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
