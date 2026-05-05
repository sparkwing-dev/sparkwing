package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
)

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
	sparkwingDir, fetchErr := bincache.FetchPipelineSource(opts.GitcacheURL, repoURL, branch, sha, workDir)
	if fetchErr != nil {
		return awaitHeartbeat(), fmt.Errorf("fetch source: %w", fetchErr)
	}

	binPath, buildErr := triggerBuildOrFetchBinary(sparkwingDir, opts, logger)
	if buildErr != nil {
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
