package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// remoteChildMarker tells a re-entered RunNodeOnce to skip
// shouldRunRemote and run locally against the source the parent
// already cloned, breaking the otherwise-infinite clone+compile
// recursion.
const remoteChildMarker = "SPARKWING_REMOTE_CHILD"

// shouldRunRemote decides between in-process execution and the remote
// clone+compile path. Triggers carrying repo_url or GitHub metadata provide
// enough source information to compile a missing pipeline from disk.
func shouldRunRemote(trigger *store.Trigger) bool {
	if os.Getenv(remoteChildMarker) == "1" {
		return false
	}
	if trigger == nil {
		return false
	}
	return remoteTriggerSourceURLRaw(trigger) != ""
}

// runNodeRemote is RunNodeOnce's fallback for pipelines not baked
// into the calling runner binary. Clones the repo, builds (or fetches
// from /bin/<hash> cache) the pipeline binary, then execs it with
// `run-node <runID> <nodeID>`. The child writes terminal state to the
// controller; we just surface its outcome.
func runNodeRemote(
	ctx context.Context,
	trigger *store.Trigger,
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

	repoURL, sourceErr := remoteTriggerSourceURL(trigger)
	if sourceErr != nil {
		return runner.Result{}, sourceErr
	}
	if repoURL == "" {
		return runner.Result{},
			fmt.Errorf("pipeline %q not registered locally, and trigger has no repo_url for remote fallback",
				run.Pipeline)
	}
	branch := trigger.GitBranch
	if branch == "" {
		branch = run.GitBranch
	}
	if branch == "" {
		branch = "main"
	}

	logger.Info("runNodeRemote: fetching source",
		"run_id", runID, "node_id", nodeID, "repo", sourceurl.Redact(repoURL), "branch", branch)

	workDir := filepath.Join(bincache.SparkwingHome(), "node-runner", runID+"-"+nodeID)
	defer func() { _ = os.RemoveAll(workDir) }()

	sparkwingDir, err := bincache.FetchPipelineSource(gcURL, repoURL, branch, trigger.GitSHA, workDir)
	if err != nil {
		return runner.Result{}, fmt.Errorf("fetch source: %w", err)
	}

	binPath, err := resolveRemoteBinary(sparkwingDir, gcURL, bincache.CacheToken(), logger)
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	logger.Info("runNodeRemote: binary ready",
		"run_id", runID, "node_id", nodeID, "bin", binPath)

	childEnv := append(
		os.Environ(),
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
		logger.Warn("runNodeRemote: child exited non-zero",
			"run_id", runID, "node_id", nodeID, "err", err)
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("child run-node: %w", err),
		}, nil
	}
	return runner.Result{Outcome: sparkwing.Success}, nil
}

func remoteTriggerSourceURL(trigger *store.Trigger) (string, error) {
	raw := remoteTriggerSourceURLRaw(trigger)
	if raw == "" {
		return "", nil
	}
	return sourceurl.ValidateCloneURL(raw)
}

func remoteTriggerSourceURLRaw(trigger *store.Trigger) string {
	if trigger == nil {
		return ""
	}
	repo := trigger.TriggerEnv["GITHUB_REPOSITORY"]
	if repo == "" && trigger.GithubOwner != "" && trigger.GithubRepo != "" {
		repo = trigger.GithubOwner + "/" + trigger.GithubRepo
	}
	if repo != "" {
		return bincache.RepoURLFromGitHub(repo)
	}
	return trigger.RepoURL
}

// resolveRemoteBinary tries local disk, then remote /bin/<hash>, then
// compile+upload, mirroring the trigger loop's build/upload dance.
func resolveRemoteBinary(sparkwingDir, gcURL, token string, logger *slog.Logger) (string, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
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
