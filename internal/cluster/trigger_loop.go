package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/logs"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
)

// CompileLogNode is the synthetic node id used to attribute the
// trigger loop's `go build` stdout + stderr to the run's structured
// logs when the .sparkwing/ compile fails (IMP-001). The compile
// step happens before any pipeline binary runs, so there's no real
// orchestrator node to attach the output to; this fixed id lets
// `sparkwing runs logs --run <id>` surface the toolchain error
// without operators having to kubectl-log the warm-runner pod.
const CompileLogNode = "_compile"

// TriggerLoopOptions configures RunTriggerLoop.
type TriggerLoopOptions struct {
	ControllerURL string
	LogsURL       string
	GitcacheURL   string
	Token         string
	WorkRoot      string
	Poll          time.Duration
	Logger        *slog.Logger
	// Sources filters claim requests by trigger_source; empty = any.
	// The warm-runner sets ["github"] so it only claims webhook-originated
	// triggers and doesn't swallow manual/schedule work.
	Sources []string
}

// RunTriggerLoop claims pending triggers and dispatches via
// `handle-trigger <id>` child exec. Triggers with GITHUB_REPOSITORY
// fetch source via sparkwing-cache; triggers without a repo exec the
// baked pipeline binary. Blocks until ctx is canceled.
func RunTriggerLoop(ctx context.Context, opts TriggerLoopOptions) error {
	if opts.ControllerURL == "" {
		return errors.New("TriggerLoopOptions.ControllerURL required")
	}
	if opts.GitcacheURL == "" {
		return errors.New("TriggerLoopOptions.GitcacheURL required")
	}
	if opts.Poll <= 0 {
		opts.Poll = time.Second
	}
	if opts.WorkRoot == "" {
		opts.WorkRoot = filepath.Join(bincache.SparkwingHome(), "trigger-loop")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(opts.WorkRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir work-root: %w", err)
	}

	cli := client.NewWithToken(opts.ControllerURL, nil, opts.Token)
	logger.Info("trigger loop started",
		"controller", opts.ControllerURL,
		"gitcache", opts.GitcacheURL,
		"poll", opts.Poll,
		"work_root", opts.WorkRoot,
	)

	for {
		if err := ctx.Err(); err != nil {
			logger.Info("trigger loop shutting down", "reason", err)
			return nil
		}

		trigger, err := cli.ClaimTriggerFor(ctx, nil, opts.Sources)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			logger.Error("trigger loop: claim failed", "err", err)
			sleepOrCancel(ctx, opts.Poll)
			continue
		}
		if trigger == nil {
			sleepOrCancel(ctx, opts.Poll)
			continue
		}
		logger.Info("trigger loop: claimed",
			"run_id", trigger.ID,
			"pipeline", trigger.Pipeline,
			"repo", trigger.TriggerEnv["GITHUB_REPOSITORY"])

		selfTerminate, err := handleOneTrigger(ctx, cli, trigger, opts, logger)
		if err != nil {
			logger.Error("trigger loop: trigger failed",
				"run_id", trigger.ID, "err", err)
			// IMP-004: pre-orchestrator failures (fetch / compile /
			// no-baked-binary) never reach orchestrator.Run, which is
			// where FinishRun would normally fire. Mark the
			// controller-pre-allocated Run row failed here so the
			// operator sees status=failed + the wrapper error in
			// `runs list` / `runs status` instead of a stuck pending
			// row. Use a fresh ctx; the trigger ctx may already be
			// shutting down. Best-effort: a failed write logs and
			// moves on, since the trigger is about to be finished.
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if ferr := cli.FinishRun(finishCtx, trigger.ID, "failed", err.Error()); ferr != nil {
				logger.Warn("trigger loop: FinishRun failed",
					"run_id", trigger.ID, "err", ferr)
			}
			cancel()
			// Best-effort finish so a broken trigger doesn't get re-claimed forever.
			_ = cli.FinishTrigger(ctx, trigger.ID)
		}
		if selfTerminate {
			logger.Error("trigger loop: self-terminating after prolonged controller silence",
				"run_id", trigger.ID)
			return nil
		}
	}
}

// BakedBinary is the path to a baked-in pipeline binary used for
// triggers without a repo. Empty disables the no-repo path.
var BakedBinary = os.Getenv("SPARKWING_BAKED_BINARY")

func handleOneTrigger(ctx context.Context, cli *client.Client, trigger *store.Trigger, opts TriggerLoopOptions, logger *slog.Logger) (selfTerminate bool, err error) {
	ctx, span := otelutil.Tracer("sparkwing-trigger-loop").Start(ctx, "handleOneTrigger")
	defer span.End()
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{
		RunID:    trigger.ID,
		Pipeline: trigger.Pipeline,
	})

	// Prefer the env var (set by webhook intake); fall back to owner/repo fields.
	repo := trigger.TriggerEnv["GITHUB_REPOSITORY"]
	if repo == "" && trigger.GithubOwner != "" && trigger.GithubRepo != "" {
		repo = trigger.GithubOwner + "/" + trigger.GithubRepo
	}

	// childCtx is canceled by the heartbeat goroutine on reaped/silenced
	// outcomes so the child process tears down instead of writing to a
	// run the controller already wrote off.
	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()

	outcomeCh := make(chan triggerClaimOutcome, 1)
	go func() {
		outcomeCh <- triggerClaimHeartbeat(childCtx, cli, trigger.ID, cancelChild, logger)
	}()

	// awaitHeartbeat returns whether the runner should self-terminate.
	// Always called exactly once per trigger so the goroutine is
	// joined before we return.
	awaitHeartbeat := func() bool {
		cancelChild()
		outcome := <-outcomeCh
		return outcome == triggerClaimSilenced
	}

	// Baked-in path: trigger has no repo (synthetic POST trigger,
	// internal test, etc). Exec the image's own baked binary so the
	// demo pipelines registered in its .sparkwing/ module are
	// visible to Plan(). No gitcache fetch, no compile.
	if repo == "" {
		if BakedBinary == "" {
			return awaitHeartbeat(), fmt.Errorf("trigger %s has no GITHUB_REPOSITORY and SPARKWING_BAKED_BINARY is unset (no in-image pipeline binary to fall back on)", trigger.ID)
		}
		execErr := execHandleTrigger(childCtx, BakedBinary, "", trigger, opts, logger)
		return awaitHeartbeat(), execErr
	}

	repoURL := bincache.RepoURLFromGitHub(repo)
	branch := trigger.GitBranch
	if branch == "" {
		branch = "main"
	}

	workDir := filepath.Join(opts.WorkRoot, trigger.ID)
	defer os.RemoveAll(workDir)

	sha := trigger.GitSHA
	logger.Info("trigger loop: fetching source",
		"run_id", trigger.ID, "repo", repoURL, "branch", branch, "sha", sha)
	if sha == "" {
		// Manual CLI dispatch (no webhook payload) lands here. The
		// runner builds the branch tip rather than a pinned commit;
		// flag it so operators can correlate "the dashboard says X
		// but I expected Y" against this code path.
		logger.Info("trigger loop: no trigger SHA, falling back to branch-tip clone",
			"run_id", trigger.ID, "branch", branch)
	}
	sparkwingDir, fetchErr := fetchPipelineSourceWithRetry(ctx, opts.GitcacheURL, repoURL, branch, sha, workDir, logger, trigger.ID)
	if fetchErr != nil {
		return awaitHeartbeat(), fmt.Errorf("fetch source: %w", fetchErr)
	}

	binPath, buildErr := triggerBuildOrFetchBinary(sparkwingDir, opts, logger)
	if buildErr != nil {
		shipCompileOutput(ctx, opts, trigger.ID, buildErr, logger)
		return awaitHeartbeat(), buildErr
	}
	logger.Info("trigger loop: binary ready",
		"run_id", trigger.ID, "bin", binPath)

	execErr := execHandleTrigger(childCtx, binPath, filepath.Dir(sparkwingDir), trigger, opts, logger)
	return awaitHeartbeat(), execErr
}

// execHandleTrigger runs the child pipeline binary with workDir as cwd.
// Git metadata isn't forwarded via env; the child reads the cloned .git directly.
func execHandleTrigger(ctx context.Context, binPath, workDir string, trigger *store.Trigger, opts TriggerLoopOptions, logger *slog.Logger) error {
	childArgs := []string{
		"handle-trigger", trigger.ID,
		"--controller", opts.ControllerURL,
		"--token", opts.Token,
	}
	if opts.LogsURL != "" {
		childArgs = append(childArgs, "--logs", opts.LogsURL)
	}

	childCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binPath, childArgs...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	env := append(os.Environ(),
		"SPARKWING_CONTROLLER_URL="+opts.ControllerURL,
		"SPARKWING_LOGS_URL="+opts.LogsURL,
		"SPARKWING_AGENT_TOKEN="+opts.Token,
		// Cluster-side marker; CurrentRunConfig.IsLocal=false for the child.
		"SPARKWING_HOST=cluster",
	)
	if tp := otelutil.TraceParentEnv(ctx); tp != "" {
		env = append(env, tp)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logger.Info("trigger loop: exec child",
		"trigger_id", trigger.ID, "bin", binPath, "dir", workDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("child pipeline binary: %w", err)
	}
	return nil
}

// shipCompileOutput posts the captured `go build` output for a
// trigger whose .sparkwing/ compile failed into a synthetic
// CompileLogNode log on the controller's logs service. Best-effort:
// we already have a wrapper error to return, so a failed POST
// degrades silently to a warning -- IMP-002 tracks the broader
// "logs.append silent-success" hardening.
func shipCompileOutput(ctx context.Context, opts TriggerLoopOptions, runID string, buildErr error, logger *slog.Logger) {
	if opts.LogsURL == "" {
		return
	}
	var ce *bincache.CompileError
	if !errors.As(buildErr, &ce) || len(ce.Output) == 0 {
		return
	}
	cli := logs.NewClientWithToken(opts.LogsURL, nil, opts.Token)
	// Use a fresh context: the trigger ctx may already be cancelling
	// (heartbeat goroutine signalled the parent), but we still want
	// to ship the diagnostic so operators can see why compile failed.
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := cli.Append(postCtx, runID, CompileLogNode, ce.Output); err != nil {
		logger.Warn("trigger loop: failed to ship compile output to logs",
			"run_id", runID, "err", err)
	}
}

// fetchSourceFn is the indirection used by fetchPipelineSourceWithRetry
// so tests can substitute a fake that fails the first N times. Production
// code uses bincache.FetchPipelineSource directly.
var fetchSourceFn = bincache.FetchPipelineSource

// Vars (not consts) so tests can shrink the retry surface.
//
// IMP-005: the warm-runner's source fetch races the gitcache's 30s
// background-fetch loop on the `git push && wing X --on prod` path.
// 3 attempts spaced ~10s apart (so total wall time ≤ 30s, the
// background-fetch period) recovers the residual case where the
// dispatch-time eager refresh either failed or got skipped (e.g. the
// laptop profile has no gitcache URL configured).
var (
	triggerFetchMaxAttempts = 3
	triggerFetchRetryDelay  = 10 * time.Second
)

// notOurRefSubstr is the marker we match in fetch errors to decide
// "this is the gitcache catching up" vs. "this is a real failure".
// Documented at the call site because git's wording could change in
// a future release; if it does, the symptom is that retries stop
// firing and operators see the original cryptic error again.
const notOurRefSubstr = "not our ref"

// fetchPipelineSourceWithRetry wraps bincache.FetchPipelineSource
// with bounded retry on the gitcache-lag failure mode. Other errors
// (auth, missing repo, malformed URL, etc.) fail fast — we never
// want to delay surfacing an obviously-broken state by 30s.
//
// On exhausted retries the caller still gets the original error
// chain (so errors.Is / errors.As keep working), wrapped in a
// human-readable message that names the SHA and points at the
// gitcache-lag root cause instead of leaving operators staring at
// "fatal: remote error: upload-pack: not our ref".
func fetchPipelineSourceWithRetry(ctx context.Context, gcURL, repoURL, branch, sha, workDir string, logger *slog.Logger, runID string) (string, error) {
	attempts := triggerFetchMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		sparkwingDir, err := fetchSourceFn(gcURL, repoURL, branch, sha, workDir)
		if err == nil {
			return sparkwingDir, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), notOurRefSubstr) {
			// Real failure — don't delay surfacing it.
			return "", err
		}
		if i == attempts-1 {
			break
		}
		logger.Warn("trigger loop: gitcache lagging; retrying source fetch",
			"run_id", runID, "sha", sha, "attempt", i+1, "of", attempts, "err", err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(triggerFetchRetryDelay):
		}
	}
	return "", fmt.Errorf("SHA %s not yet in gitcache after %d attempts; the background fetch may not have completed since the push: %w",
		sha, attempts, lastErr)
}

func triggerBuildOrFetchBinary(sparkwingDir string, opts TriggerLoopOptions, logger *slog.Logger) (string, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		tmp := filepath.Join(sparkwingDir, ".sparkwing-trigger-loop-bin")
		if cerr := bincache.CompilePipeline(sparkwingDir, tmp); cerr != nil {
			return "", cerr
		}
		return tmp, nil
	}
	binPath := bincache.CachedBinaryPath(key)
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}
	if err := bincache.TryBinary(opts.GitcacheURL, key, binPath); err == nil {
		return binPath, nil
	} else if !errors.Is(err, bincache.ErrMiss) {
		logger.Warn("trigger loop: bin cache fetch failed; compiling", "err", err, "hash", key)
	}
	if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
		return "", err
	}
	if err := bincache.UploadBinary(opts.GitcacheURL, opts.Token, key, binPath); err != nil {
		logger.Warn("trigger loop: bin cache upload failed", "err", err, "hash", key)
	}
	return binPath, nil
}

type triggerClaimOutcome int

const (
	triggerClaimCtxDone triggerClaimOutcome = iota
	// triggerClaimReaped: controller 404'd a heartbeat (lease lost). Child killed.
	triggerClaimReaped
	// triggerClaimSilenced: no successful heartbeat for maxTriggerHeartbeatSilence.
	// Child killed; runner should self-terminate.
	triggerClaimSilenced
)

// Vars (not consts) so tests can shrink them.
var (
	// Matches store.DefaultLeaseDuration so reaper + heartbeat decide simultaneously.
	maxTriggerHeartbeatSilence = 3 * time.Minute
	triggerHeartbeatInterval   = 3 * time.Second
	// Strictly less than the interval so a wedged controller can't stack ticks.
	triggerHeartbeatTimeout = 2 * time.Second
)

// triggerClaimHeartbeat extends the lease until ctx cancels. Kills the
// child and returns non-ctxDone on a 404 or ≥maxTriggerHeartbeatSilence
// of consecutive heartbeat failures.
func triggerClaimHeartbeat(ctx context.Context, cli *client.Client, triggerID string, killChild context.CancelFunc, logger *slog.Logger) triggerClaimOutcome {
	t := time.NewTicker(triggerHeartbeatInterval)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			return triggerClaimCtxDone
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(ctx, triggerHeartbeatTimeout)
			_, err := cli.HeartbeatTrigger(hbCtx, triggerID)
			cancel()
			if err == nil {
				lastOK = time.Now()
				continue
			}
			if errors.Is(err, context.Canceled) {
				return triggerClaimCtxDone
			}
			if errors.Is(err, store.ErrNotFound) {
				logger.Error("trigger loop: trigger reaped by controller; killing child",
					"trigger_id", triggerID)
				killChild()
				return triggerClaimReaped
			}
			silence := time.Since(lastOK)
			if silence >= maxTriggerHeartbeatSilence {
				logger.Error("trigger loop: controller unreachable beyond lease window; killing child and self-terminating",
					"trigger_id", triggerID,
					"silence", silence.Round(time.Second),
					"err", err)
				killChild()
				return triggerClaimSilenced
			}
			logger.Warn("trigger loop: heartbeat failed",
				"trigger_id", triggerID,
				"err", err,
				"silence", silence.Round(time.Second))
		}
	}
}
