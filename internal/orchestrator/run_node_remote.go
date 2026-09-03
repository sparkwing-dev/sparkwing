package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const remoteChildMarker = "SPARKWING_REMOTE_CHILD"

func shouldRunRemote(trigger *store.Trigger) bool {
	if os.Getenv(remoteChildMarker) == "1" {
		return false
	}
	if trigger == nil {
		return false
	}
	return remoteTriggerSourceURLRaw(trigger) != ""
}

func runNodeRemote(
	ctx context.Context,
	trigger *store.Trigger,
	run *store.Run,
	controllerURL, logsURL, gitcacheURL, cacheToken, runID, nodeID, token string,
	logger *slog.Logger,
) (runner.Result, error) {
	gcURL := strings.TrimRight(gitcacheURL, "/")
	if gcURL == "" {
		gcURL = bincache.CacheURL()
	}
	if gcURL == "" {
		return runner.Result{},
			fmt.Errorf("pipeline %q not registered in this runner image, and SPARKWING_GITCACHE_URL is unset so we cannot fall back to remote compile",
				run.Pipeline)
	}
	gcURL = bincache.ControllerRunGitcacheURL(gcURL, controllerURL, runID)

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
	// #nosec G703 -- a work directory under this user's own Sparkwing home
	defer func() { _ = os.RemoveAll(workDir) }()
	if err := fssecure.EnsureDir(workDir); err != nil {
		return runner.Result{}, fmt.Errorf("create private work directory: %w", err)
	}

	fetchSource := bincache.FetchPipelineSourceWithCredentials
	if strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") {
		fetchSource = bincache.FetchPipelineWorkspaceSourceWithCredentials
	}
	sparkwingDir, err := fetchSource(gcURL, controllerURL, token, cacheToken,
		repoURL, branch, trigger.GitSHA, workDir)
	if err != nil {
		return runner.Result{}, fmt.Errorf("fetch source: %w", err)
	}

	binaryCacheURL := gcURL
	if bincache.ControllerGitcacheToken(gcURL, controllerURL, token) != "" {
		binaryCacheURL = ""
	}
	if cacheToken == "" {
		cacheToken = bincache.CacheToken()
	}
	binary, err := resolveRemoteBinary(sparkwingDir, binaryCacheURL, cacheToken, logger)
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	defer binary.release()
	logger.Info("runNodeRemote: binary ready",
		"run_id", runID, "node_id", nodeID, "bin", binary.path)

	childEnv := append(
		os.Environ(),
		"SPARKWING_CONTROLLER_URL="+controllerURL,
		"SPARKWING_LOGS_URL="+logsURL,
		"SPARKWING_AGENT_TOKEN="+token,
		remoteChildMarker+"=1",
	)
	if holderID, ok := nodeClaimHolder(ctx); ok {
		childEnv = append(childEnv, "SPARKWING_NODE_CLAIM_HOLDER="+holderID)
	}

	// #nosec G702 -- the node runner binary this process resolved, run as argv without a shell
	cmd := exec.CommandContext(ctx, binary.path, "run-node", runID, nodeID)
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

type remoteBinary struct {
	path  string
	lease *bincache.Lease
}

func (b remoteBinary) release() {
	if b.lease != nil {
		_ = b.lease.Release()
	}
}

func resolveRemoteBinary(sparkwingDir, gcURL, token string, logger *slog.Logger) (remoteBinary, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		tmp := filepath.Join(sparkwingDir, ".sparkwing-runner-bin")
		if cerr := bincache.CompilePipeline(sparkwingDir, tmp); cerr != nil {
			return remoteBinary{}, cerr
		}
		return remoteBinary{path: tmp}, nil
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return remoteBinary{}, err
	}
	compiled := false
	lease, published, err := entry.AcquireOrMaterialize(context.Background(), func(tempPath string) error {
		if gcURL != "" {
			if fetchErr := bincache.TryBinary(gcURL, token, key, tempPath); fetchErr == nil {
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				logger.Warn("runNodeRemote: bin cache fetch failed; compiling", "err", fetchErr, "hash", key)
			}
		}
		compiled = true
		return bincache.CompilePipeline(sparkwingDir, tempPath)
	})
	if err != nil {
		return remoteBinary{}, err
	}
	if published && compiled && gcURL != "" {
		if err := bincache.UploadBinary(gcURL, token, key, lease.Path()); err != nil {
			logger.Warn("runNodeRemote: bin cache upload failed", "err", err, "hash", key)
		}
	}
	return remoteBinary{path: lease.Path(), lease: lease}, nil
}
