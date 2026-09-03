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
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	remoteExecutionCapabilityInputEnv = "SPARKWING_EXECUTION_CAPABILITY_STDIN"
	remoteBrokeredArtifactEnv         = "SPARKWING_BROKERED_ARTIFACTS"
)

func shouldRunRemote(trigger *store.Trigger, brokeredChild bool) bool {
	if brokeredChild {
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
	return runNodeChild(ctx, binary.path, filepath.Dir(sparkwingDir), controllerURL, logsURL, token, runID, nodeID, logger)
}

func runNodeIsolated(
	ctx context.Context,
	controllerURL, logsURL, runID, nodeID, token string,
	logger *slog.Logger,
) (runner.Result, error) {
	binary, err := os.Executable()
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve runner executable: %w", err)
	}
	dir := sparkwing.CurrentRuntime().WorkDir
	if dir == "" {
		dir, err = os.Getwd()
		if err != nil {
			return runner.Result{}, fmt.Errorf("resolve runner work directory: %w", err)
		}
	}
	return runNodeChild(ctx, binary, dir, controllerURL, logsURL, token, runID, nodeID, logger)
}

var runNodeIsolatedFn = runNodeIsolated

func runNodeChild(
	ctx context.Context,
	binary, dir, controllerURL, logsURL, token, runID, nodeID string,
	logger *slog.Logger,
) (runner.Result, error) {

	fence, _ := store.NodeClaimFenceFromContext(ctx)
	artifact, err := resolveArtifactStoreFromEnv(ctx)
	if err != nil {
		return runner.Result{}, fmt.Errorf("open supervisor artifact store: %w", err)
	}
	broker, err := startRemoteExecutionBroker(controllerURL, logsURL, token, runID, nodeID, fence, artifact, logger)
	if err != nil {
		return runner.Result{}, err
	}
	defer broker.Close()
	childLogsURL := ""
	if logsURL != "" {
		childLogsURL = broker.URL()
	}
	childBaseEnv, err := remoteExecutionChildEnvironment(os.Environ())
	if err != nil {
		return runner.Result{}, err
	}
	childEnv := append(
		childBaseEnv,
		"SPARKWING_CONTROLLER_URL="+broker.URL(),
		"SPARKWING_LOGS_URL="+childLogsURL,
		remoteExecutionCapabilityInputEnv+"=1",
	)
	if fence.ClaimGeneration > 0 {
		childEnv = append(childEnv, remoteBrokeredClaimEnv+"=1")
	}
	if artifact != nil {
		childEnv = append(childEnv, remoteBrokeredArtifactEnv+"=1")
	}

	// #nosec G702 -- the node runner binary this process resolved, run as argv without a shell
	cmd := exec.CommandContext(ctx, binary, "run-node", runID, nodeID)
	cmd.Dir = dir
	cmd.Env = childEnv
	cmd.Stdin = strings.NewReader(broker.capability + "\n")
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

func remoteExecutionChildEnvironment(env []string) ([]string, error) {
	names, prefixes, err := submissionEnvironmentAllowList(env)
	if err != nil {
		return nil, fmt.Errorf("remote execution environment allowlist: %w", err)
	}
	out := make([]string, 0, len(env))
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if !ok || remoteExecutionPrivateEnv[name] || name == submissionEnvironmentAllowKey {
			continue
		}
		if !remoteExecutionRuntimeEnv[name] && !submissionEnvironmentAllowed(name, names, prefixes) {
			continue
		}
		if envredact.CredentialName(name) || envredact.CredentialValue(value) || envredact.RedactValue(value) != value {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

var remoteExecutionRuntimeEnv = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"LANG": true, "LC_ALL": true, "SYSTEMROOT": true, "COMSPEC": true,
	"PATHEXT": true, "USERPROFILE": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

var remoteExecutionPrivateEnv = map[string]bool{
	"SPARKWING_AGENT_TOKEN":              true,
	"SPARKWING_CONTROLLER_URL":           true,
	"SPARKWING_LOGS_URL":                 true,
	ArtifactStoreEnvVar:                  true,
	"SPARKWING_CACHE_TOKEN":              true,
	remoteExecutionCapabilityEnv:         true,
	remoteExecutionCapabilityInputEnv:    true,
	remoteBrokeredArtifactEnv:            true,
	remoteBrokeredClaimEnv:               true,
	"SPARKWING_NODE_CLAIM_HOLDER":        true,
	"SPARKWING_NODE_CLAIM_GENERATION":    true,
	"SPARKWING_NODE_CLAIM_MEMBERSHIP":    true,
	"SPARKWING_NODE_CLAIM_RESERVATION":   true,
	"SPARKWING_TRIGGER_CLAIM_GENERATION": true,
	"SPARKWING_TRIGGER_GENERATION":       true,
	"SPARKWING_ATTEMPT_ORDINAL":          true,
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
