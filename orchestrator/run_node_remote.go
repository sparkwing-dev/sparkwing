package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/bincache"
	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// remoteChildMarker tells a re-entered RunNodeOnce to skip
// shouldRunRemote and run locally against the source the parent
// already cloned, breaking the otherwise-infinite clone+compile
// recursion.
const remoteChildMarker = "SPARKWING_REMOTE_CHILD"

// shouldRunRemote decides between in-process execution and the remote
// clone+compile path. Triggers from GitHub webhooks carry
// GITHUB_REPOSITORY, the signal that source is needed on disk.
// Best-effort: trigger fetch errors fall back to local execution.
func shouldRunRemote(ctx context.Context, stateClient *client.Client, runID string) bool {
	if os.Getenv(remoteChildMarker) == "1" {
		return false
	}
	trig, err := stateClient.GetTrigger(ctx, runID)
	if err != nil {
		return false
	}
	return trig.TriggerEnv["GITHUB_REPOSITORY"] != ""
}

// runNodeRemote is RunNodeOnce's fallback for pipelines not baked
// into the calling runner binary. Clones the repo, builds (or fetches
// from /bin/<hash> cache) the pipeline binary, then execs it with
// `run-node <runID> <nodeID>`. The child writes terminal state to the
// controller; we just surface its outcome.
func runNodeRemote(
	ctx context.Context,
	stateClient *client.Client,
	run *store.Run,
	controllerURL, logsURL, runID, nodeID, token string,
	logger *slog.Logger,
) (runner.Result, error) {
	gcURL := bincache.CacheURL()
	if gcURL == "" {
		return runner.Result{},
			fmt.Errorf("pipeline %q not registered in this runner image, and SPARKWING_GITCACHE_URL is unset so we cannot fall back to remote compile",
				run.Pipeline)
	}

	trig, err := stateClient.GetTrigger(ctx, runID)
	if err != nil {
		return runner.Result{},
			fmt.Errorf("pipeline %q not registered locally; fetching trigger for remote fallback: %w",
				run.Pipeline, err)
	}
	repo := trig.TriggerEnv["GITHUB_REPOSITORY"]
	if repo == "" {
		return runner.Result{},
			fmt.Errorf("pipeline %q not registered locally, and trigger has no GITHUB_REPOSITORY for remote fallback",
				run.Pipeline)
	}
	repoURL := bincache.RepoURLFromGitHub(repo)
	branch := trig.GitBranch
	if branch == "" {
		branch = run.GitBranch
	}
	if branch == "" {
		branch = "main"
	}

	logger.Info("runNodeRemote: fetching source",
		"run_id", runID, "node_id", nodeID, "repo", repoURL, "branch", branch)

	// Scoped per run+node so concurrent remote fallbacks on the same
	// pod don't collide.
	workDir := filepath.Join(bincache.SparkwingHome(), "node-runner", runID+"-"+nodeID)
	defer os.RemoveAll(workDir)

	sparkwingDir, err := bincache.FetchPipelineSource(gcURL, repoURL, branch, trig.GitSHA, workDir)
	if err != nil {
		return runner.Result{}, fmt.Errorf("fetch source: %w", err)
	}

	binPath, err := resolveRemoteBinary(sparkwingDir, gcURL, bincache.CacheToken(), logger)
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	logger.Info("runNodeRemote: binary ready",
		"run_id", runID, "node_id", nodeID, "bin", binPath)

	// cmd.Dir below must be the repo root so the SDK's walk-up to
	// .sparkwing/ resolves.
	childEnv := append(os.Environ(),
		"SPARKWING_CONTROLLER_URL="+controllerURL,
		"SPARKWING_LOGS_URL="+logsURL,
		"SPARKWING_AGENT_TOKEN="+token,
		remoteChildMarker+"=1",
	)

	cmd := exec.CommandContext(ctx, binPath, "run-node", runID, nodeID)
	cmd.Dir = filepath.Dir(sparkwingDir)
	cmd.Env = childEnv
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Child already wrote terminal state; surface a Result so the
		// caller's logging matches the local path's shape.
		logger.Warn("runNodeRemote: child exited non-zero",
			"run_id", runID, "node_id", nodeID, "err", err)
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("child run-node: %w", err),
		}, nil
	}
	return runner.Result{Outcome: sparkwing.Success}, nil
}

// resolveRemoteBinary tries local disk, then remote /bin/<hash>, then
// compile+upload, mirroring the trigger loop's build/upload dance.
func resolveRemoteBinary(sparkwingDir, gcURL, token string, logger *slog.Logger) (string, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		// Compile into a scoped path so a transient hash error doesn't
		// poison the cache.
		tmp := filepath.Join(sparkwingDir, ".sparkwing-runner-bin")
		if cerr := bincache.CompilePipeline(sparkwingDir, tmp); cerr != nil {
			return "", cerr
		}
		return tmp, nil
	}
	binPath := bincache.CachedBinaryPath(key)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	if gcURL != "" {
		if err := bincache.TryBinary(gcURL, key, binPath); err == nil {
			return binPath, nil
		} else if !errors.Is(err, bincache.ErrMiss) {
			logger.Warn("runNodeRemote: bin cache fetch failed; compiling", "err", err, "hash", key)
		}
	}

	if err := bincache.CompilePipeline(sparkwingDir, binPath); err != nil {
		return "", err
	}
	if gcURL != "" {
		if err := bincache.UploadBinary(gcURL, token, key, binPath); err != nil {
			logger.Warn("runNodeRemote: bin cache upload failed", "err", err, "hash", key)
		}
	}
	return binPath, nil
}
