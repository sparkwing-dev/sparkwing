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
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const CompileLogNode = "_compile"

type TriggerLoopOptions struct {
	ControllerURL   string
	LogsURL         string
	GitcacheURL     string
	Token           string
	RunnerKind      string
	K8sNamespace    string
	K8sImage        string
	K8sRunnerSA     string
	K8sPullSecret   string
	K8sCtrlURL      string
	K8sLogsURL      string
	Kubeconfig      string
	ArtifactStore   string
	K8sNodeSelector []string
	K8sTolerations  []string

	DependencyProxy string

	K8sImagePullPolicy string
	WorkRoot           string
	Poll               time.Duration
	Logger             *slog.Logger

	MaxConcurrent int

	Sources []string
}

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
	if opts.MaxConcurrent < 1 {
		opts.MaxConcurrent = 4
	}
	privateWorkRoot := opts.WorkRoot == ""
	if privateWorkRoot {
		opts.WorkRoot = filepath.Join(bincache.SparkwingHome(), "trigger-loop")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if err := ensureTriggerWorkRoot(opts.WorkRoot, privateWorkRoot); err != nil {
		return fmt.Errorf("mkdir work-root: %w", err)
	}

	cli := client.NewWithToken(opts.ControllerURL, nil, opts.Token)
	logger.Info(
		"trigger loop started",
		"controller", opts.ControllerURL,
		"gitcache", opts.GitcacheURL,
		"poll", opts.Poll,
		"work_root", opts.WorkRoot,
		"max_concurrent", opts.MaxConcurrent,
	)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, opts.MaxConcurrent)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		if err := ctx.Err(); err != nil {
			logger.Info("trigger loop shutting down", "reason", err)
			return nil
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil
		}

		trigger, err := cli.ClaimTriggerFor(ctx, nil, opts.Sources)
		if err != nil {
			<-sem
			if errors.Is(err, context.Canceled) {
				return nil
			}
			logger.Error("trigger loop: claim failed", "err", err)
			sleepOrCancel(ctx, opts.Poll)
			continue
		}
		if trigger == nil {
			<-sem
			sleepOrCancel(ctx, opts.Poll)
			continue
		}
		logger.Info("trigger loop: claimed",
			"run_id", trigger.ID,
			"pipeline", trigger.Pipeline,
			"repo", trigger.TriggerEnv["GITHUB_REPOSITORY"])

		wg.Add(1)
		go func(trigger *store.Trigger) {
			defer wg.Done()
			defer func() { <-sem }()

			selfTerminate, err := handleOneTrigger(ctx, cli, trigger, opts, logger)
			if err != nil {
				logger.Error("trigger loop: trigger failed",
					"run_id", trigger.ID, "err", err)
				finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				if ferr := cli.FinishRun(finishCtx, trigger.ID, "failed", err.Error()); ferr != nil {
					logger.Warn("trigger loop: FinishRun failed",
						"run_id", trigger.ID, "err", ferr)
				}
				finishCancel()
				_ = cli.FinishTrigger(context.WithoutCancel(ctx), trigger.ID)
			}
			if selfTerminate {
				logger.Error("trigger loop: self-terminating after prolonged controller silence",
					"run_id", trigger.ID)
				cancel()
			}
		}(trigger)
	}
}

func ensureTriggerWorkRoot(path string, private bool) error {
	if private {
		return fssecure.EnsureDir(path)
	}
	return os.MkdirAll(path, 0o755)
}

var BakedBinary = os.Getenv("SPARKWING_BAKED_BINARY")

func handleOneTrigger(ctx context.Context, cli *client.Client, trigger *store.Trigger, opts TriggerLoopOptions, logger *slog.Logger) (selfTerminate bool, err error) {
	ctx, span := otelutil.Tracer("sparkwing-trigger-loop").Start(ctx, "handleOneTrigger")
	defer span.End()
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{
		RunID:    trigger.ID,
		Pipeline: trigger.Pipeline,
	})

	repoURL, sourceErr := triggerSourceURL(trigger)

	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()

	outcomeCh := make(chan triggerClaimOutcome, 1)
	go func() {
		outcomeCh <- triggerClaimHeartbeat(childCtx, cli, trigger.ID, cancelChild, logger)
	}()

	awaitHeartbeat := func() bool {
		cancelChild()
		outcome := <-outcomeCh
		return outcome == triggerClaimSilenced
	}

	if sourceErr != nil {
		return awaitHeartbeat(), sourceErr
	}
	if repoURL == "" {
		if BakedBinary == "" {
			return awaitHeartbeat(), fmt.Errorf("trigger %s has no repo_url and SPARKWING_BAKED_BINARY is unset (no in-image pipeline binary to fall back on)", trigger.ID)
		}
		execErr := execHandleTrigger(childCtx, BakedBinary, "", trigger, opts, logger)
		return awaitHeartbeat(), execErr
	}

	branch := trigger.GitBranch
	if branch == "" {
		branch = "main"
	}

	workDir := filepath.Join(opts.WorkRoot, trigger.ID)
	defer func() { _ = os.RemoveAll(workDir) }()

	sha := trigger.GitSHA
	logger.Info("trigger loop: fetching source",
		"run_id", trigger.ID, "repo", sourceurl.Redact(repoURL), "branch", branch, "sha", sha)
	if sha == "" {
		logger.Info("trigger loop: no trigger SHA, falling back to branch-tip clone",
			"run_id", trigger.ID, "branch", branch)
	}
	fetchSource := fetchPipelineSourceWithRetry
	if strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") {
		fetchSource = fetchPipelineWorkspaceSourceWithRetry
	}
	sparkwingDir, fetchErr := fetchSource(ctx, opts.GitcacheURL, opts.ControllerURL, opts.Token, repoURL, branch, sha, workDir, logger, trigger.ID)
	if fetchErr != nil {
		return awaitHeartbeat(), fmt.Errorf("fetch source: %w", fetchErr)
	}

	binary, buildErr := triggerBuildOrFetchBinary(sparkwingDir, opts, logger)
	if buildErr != nil {
		shipCompileOutput(ctx, opts, trigger.ID, buildErr, logger)
		return awaitHeartbeat(), buildErr
	}
	logger.Info("trigger loop: binary ready",
		"run_id", trigger.ID, "bin", binary.path)
	defer binary.release()

	execErr := execHandleTrigger(childCtx, binary.path, filepath.Dir(sparkwingDir), trigger, opts, logger)
	return awaitHeartbeat(), execErr
}

func execHandleTrigger(ctx context.Context, binPath, workDir string, trigger *store.Trigger, opts TriggerLoopOptions, logger *slog.Logger) error {
	childArgs := handleTriggerArgs(trigger.ID, opts)

	childCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(childCtx, binPath, childArgs...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	env := append(
		os.Environ(),
		"SPARKWING_CONTROLLER_URL="+opts.ControllerURL,
		"SPARKWING_LOGS_URL="+opts.LogsURL,
		"SPARKWING_AGENT_TOKEN="+opts.Token,
		"SPARKWING_RUNNER_TYPE=kubernetes",
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

func handleTriggerArgs(triggerID string, opts TriggerLoopOptions) []string {
	childArgs := []string{
		"handle-trigger",
		"--controller", opts.ControllerURL,
		"--token", opts.Token,
	}
	if opts.LogsURL != "" {
		childArgs = append(childArgs, "--logs", opts.LogsURL)
	}
	childArgs = append(childArgs, triggerRunnerArgs(opts)...)
	childArgs = append(childArgs, triggerID)
	return childArgs
}

func triggerRunnerArgs(opts TriggerLoopOptions) []string {
	if opts.RunnerKind == "" || opts.RunnerKind == "inprocess" {
		return nil
	}
	args := []string{"--runner", opts.RunnerKind}
	appendFlag := func(name, val string) {
		if val != "" {
			args = append(args, name, val)
		}
	}
	appendFlag("--namespace", opts.K8sNamespace)
	appendFlag("--image", opts.K8sImage)
	appendFlag("--runner-sa", opts.K8sRunnerSA)
	appendFlag("--image-pull-secret", opts.K8sPullSecret)
	appendFlag("--runner-controller-url", opts.K8sCtrlURL)
	appendFlag("--runner-logs-url", opts.K8sLogsURL)
	appendFlag("--kubeconfig", opts.Kubeconfig)
	appendFlag("--artifact-store", opts.ArtifactStore)
	appendFlag("--image-pull-policy", opts.K8sImagePullPolicy)

	if opts.DependencyProxy != "" {
		args = append(args, "--dependency-proxy", opts.DependencyProxy)
	} else {
		args = append(args, "--dependency-proxy", "off")
	}
	for _, val := range opts.K8sNodeSelector {
		appendFlag("--runner-node-selector", val)
	}
	for _, val := range opts.K8sTolerations {
		appendFlag("--runner-toleration", val)
	}
	return args
}

func shipCompileOutput(ctx context.Context, opts TriggerLoopOptions, runID string, buildErr error, logger *slog.Logger) {
	if opts.LogsURL == "" {
		return
	}
	var ce *bincache.CompileError
	if !errors.As(buildErr, &ce) || len(ce.Output) == 0 {
		return
	}
	cli := logs.NewClientWithToken(opts.LogsURL, nil, opts.Token)
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := cli.Append(postCtx, runID, CompileLogNode, ce.Output); err != nil {
		logger.Warn("trigger loop: failed to ship compile output to logs",
			"run_id", runID, "err", err)
	}
}

var (
	fetchSourceFn          = bincache.FetchPipelineSourceWithToken
	fetchWorkspaceSourceFn = bincache.FetchPipelineWorkspaceSourceWithToken
)

var (
	triggerFetchMaxAttempts = 3
	triggerFetchRetryDelay  = 10 * time.Second
)

const notOurRefSubstr = "not our ref"

func fetchPipelineSourceWithRetry(ctx context.Context, gcURL, controllerURL, token, repoURL, branch, sha, workDir string, logger *slog.Logger, runID string) (string, error) {
	return fetchPipelineSourceWithRetryFn(ctx, fetchSourceFn, gcURL, controllerURL, token, repoURL, branch, sha, workDir, logger, runID)
}

func fetchPipelineWorkspaceSourceWithRetry(ctx context.Context, gcURL, controllerURL, token, repoURL, branch, sha, workDir string, logger *slog.Logger, runID string) (string, error) {
	return fetchPipelineSourceWithRetryFn(ctx, fetchWorkspaceSourceFn, gcURL, controllerURL, token, repoURL, branch, sha, workDir, logger, runID)
}

func fetchPipelineSourceWithRetryFn(ctx context.Context, fetch func(string, string, string, string, string, string, string) (string, error), gcURL, controllerURL, token, repoURL, branch, sha, workDir string, logger *slog.Logger, runID string) (string, error) {
	attempts := triggerFetchMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		sparkwingDir, err := fetch(gcURL, controllerURL, token, repoURL, branch, sha, workDir)
		if err == nil {
			return sparkwingDir, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), notOurRefSubstr) {
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

type triggerBinary struct {
	path  string
	lease *bincache.Lease
}

func (b triggerBinary) release() {
	if b.lease != nil {
		_ = b.lease.Release()
	}
}

func triggerBuildOrFetchBinary(sparkwingDir string, opts TriggerLoopOptions, logger *slog.Logger) (triggerBinary, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		tmp := filepath.Join(sparkwingDir, ".sparkwing-trigger-loop-bin")
		if cerr := bincache.CompilePipeline(sparkwingDir, tmp); cerr != nil {
			return triggerBinary{}, cerr
		}
		return triggerBinary{path: tmp}, nil
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return triggerBinary{}, err
	}
	compiled := false
	binaryCacheURL := opts.GitcacheURL
	if bincache.ControllerGitcacheToken(opts.GitcacheURL, opts.ControllerURL, opts.Token) != "" {
		binaryCacheURL = ""
	}
	lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(tempPath string) error {
		if binaryCacheURL != "" {
			if fetchErr := bincache.TryBinary(binaryCacheURL, bincache.CacheToken(), key, tempPath); fetchErr == nil {
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				logger.Warn("trigger loop: bin cache fetch failed; compiling", "err", fetchErr, "hash", key)
			}
		}
		compiled = true
		return bincache.CompilePipeline(sparkwingDir, tempPath)
	})
	if err != nil {
		return triggerBinary{}, err
	}
	if published && compiled && binaryCacheURL != "" {
		if err := bincache.UploadBinary(binaryCacheURL, bincache.CacheToken(), key, lease.Path()); err != nil {
			logger.Warn("trigger loop: bin cache upload failed", "err", err, "hash", key)
		}
	}
	return triggerBinary{path: lease.Path(), lease: lease}, nil
}

func triggerSourceURL(trigger *store.Trigger) (string, error) {
	if trigger == nil {
		return "", nil
	}
	repo := trigger.TriggerEnv["GITHUB_REPOSITORY"]
	if repo == "" && trigger.GithubOwner != "" && trigger.GithubRepo != "" {
		repo = trigger.GithubOwner + "/" + trigger.GithubRepo
	}
	if repo != "" {
		return sourceurl.ValidateCloneURL(bincache.RepoURLFromGitHub(repo))
	}
	if trigger.RepoURL != "" {
		return sourceurl.ValidateCloneURL(trigger.RepoURL)
	}
	return "", nil
}

type triggerClaimOutcome int

const (
	triggerClaimCtxDone triggerClaimOutcome = iota

	triggerClaimReaped

	triggerClaimSilenced
)

var (
	maxTriggerHeartbeatSilence = 3 * time.Minute
	triggerHeartbeatInterval   = 3 * time.Second

	triggerHeartbeatTimeout = 2 * time.Second
)

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
