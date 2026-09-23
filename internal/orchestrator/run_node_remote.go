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

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
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
	return triggerGitHubRepository(trigger) != "" || trigger.RepoURL != ""
}

func runNodeRemote(
	ctx context.Context,
	trigger *store.Trigger,
	run *store.Run,
	controllerURL, logsURL, gitcacheURL, cacheGrant, runID, nodeID, token string,
	allow *sourceurl.RepoAllowlist,
	logger *slog.Logger,
) (runner.Result, error) {
	if cacheGrant == "" {
		cacheGrant = os.Getenv(authwire.CacheGrantEnv)
	}
	gcURL := strings.TrimRight(gitcacheURL, "/")
	if gcURL == "" {
		gcURL = bincache.CacheURL()
	}
	// safety: without the operator's cache this runner fetches directly, with
	// the credential the controller releases for the run, so it never
	// borrows a cache it was not given.
	direct := gcURL == ""
	if !direct {
		gcURL = bincache.ControllerRunGitcacheURL(gcURL, controllerURL, runID)
	}

	repoURL, sourceErr := TriggerSourceURL(trigger, direct)
	if sourceErr != nil {
		return runner.Result{}, sourceErr
	}
	ownerFenced := allow != nil && !allow.Empty()
	if ownerFenced {
		if err := AdmitTriggerSource(*allow, trigger, repoURL); err != nil {
			logger.Warn("runNodeRemote: refused a node from a repository this machine does not allow",
				"run_id", runID, "node_id", nodeID, "allow_repo", allow.String(), "err", err)
			return runner.Result{}, err
		}
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

	workspaceSource := strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@")
	if direct && workspaceSource {
		return runner.Result{}, bincache.ErrWorkspaceNeedsCache
	}
	logger.Info("runNodeRemote: fetching source",
		"run_id", runID, "node_id", nodeID, "repo", sourceurl.Redact(repoURL), "branch", branch,
		"sha", trigger.GitSHA, "direct", direct)

	workDir := filepath.Join(bincache.SparkwingHome(), "node-runner", runID+"-"+nodeID)
	// #nosec G703 -- a work directory under this user's own Sparkwing home
	defer func() { _ = os.RemoveAll(workDir) }()
	if err := fssecure.EnsureDir(workDir); err != nil {
		return runner.Result{}, fmt.Errorf("create private work directory: %w", err)
	}

	var sparkwingDir string
	var err error
	switch {
	case direct:
		sparkwingDir, err = bincache.FetchRunSourceDirect(ctx, bincache.RunSource{
			ControllerURL: controllerURL, RunnerToken: token, RunID: runID,
			RepoURL: repoURL, Branch: branch, SHA: trigger.GitSHA, WorkDir: workDir,
			OwnerCredentials: ownerFenced,
		}, logger)
	case workspaceSource:
		sparkwingDir, err = bincache.FetchPipelineWorkspaceSourceWithCredentials(ctx, gcURL, controllerURL, token, cacheGrant,
			repoURL, branch, trigger.GitSHA, workDir)
	default:
		sparkwingDir, err = bincache.FetchPipelineSourceWithCredentials(ctx, gcURL, controllerURL, token, cacheGrant,
			repoURL, branch, trigger.GitSHA, workDir)
	}
	if err != nil {
		return runner.Result{}, fmt.Errorf("fetch source: %w", err)
	}
	if workspaceSource {
		adoptNodeBaseline(ctx, trigger, filepath.Dir(sparkwingDir),
			gcURL, bincache.GitcacheBearer(gcURL, controllerURL, token, cacheGrant), runID, nodeID, logger)
	}

	binaryCacheURL := gcURL
	if cacheGrant == "" || bincache.ControllerGitcacheToken(gcURL, controllerURL, token) != "" {
		binaryCacheURL = ""
	}
	binary, err := resolveRemoteBinary(ctx, sparkwingDir, controllerURL, token, binaryCacheURL, cacheGrant, logger)
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	defer binary.release()
	logger.Info("runNodeRemote: binary ready",
		"run_id", runID, "node_id", nodeID, "bin", binary.path)
	return runNodeChild(ctx, binary.path, filepath.Dir(sparkwingDir), controllerURL, logsURL, token, binaryCacheURL, cacheGrant, runID, nodeID, logger)
}

// supervisorArtifactStore is the store a node's brokered artifacts go to: the
// one the environment names, else the cache this node's grant opens. A pooled
// agent serves many runs from one process, so the grant is the node's, never
// one read from the process environment.
func supervisorArtifactStore(ctx context.Context, cacheURL, cacheGrant string) (storage.ArtifactStore, error) {
	if ResolveDevEnvURL(ArtifactStoreEnvVar) != "" || cacheURL == "" || cacheGrant == "" {
		return resolveArtifactStoreFromEnv(ctx)
	}
	return sparkwingcache.New(cacheURL, cacheGrant, nil), nil
}

func adoptNodeBaseline(ctx context.Context, trigger *store.Trigger, checkoutDir, gcURL, token, runID, nodeID string, logger *slog.Logger) {
	baseline := bincache.WorkspaceBaselineFromEnv(trigger.TriggerEnv)
	if baseline == (bincache.WorkspaceBaseline{}) {
		return
	}
	err := bincache.AdoptWorkspaceBaseline(ctx, checkoutDir, gcURL, token, trigger.GitSHA, baseline)
	switch {
	case err == nil:
		logger.Info("runNodeRemote: baseline ready",
			"run_id", runID, "node_id", nodeID, "ref", baseline.Ref, "sha", baseline.SHA)
	case errors.Is(err, bincache.ErrBaselineUnservable):
		logger.Debug("runNodeRemote: source serves only the snapshot, so it carries no baseline",
			"run_id", runID, "node_id", nodeID, "ref", baseline.Ref)
	default:
		// safety: the checkout runs without the baseline, and a step that needs it says so itself.
		logger.Warn("runNodeRemote: baseline unavailable; a step that diffs against it judges this checkout against nothing",
			"run_id", runID, "node_id", nodeID, "ref", baseline.Ref, "sha", baseline.SHA, "err", err)
	}
}

func runNodeIsolated(
	ctx context.Context,
	controllerURL, logsURL, runID, nodeID, token, cacheGrant string,
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
	return runNodeChild(ctx, binary, dir, controllerURL, logsURL, token, "", cacheGrant, runID, nodeID, logger)
}

var runNodeIsolatedFn = runNodeIsolated

// runNodeChild runs one node in the pipeline binary. The child is the team's
// own code: of the credentials this process holds it receives only the run's
// cache grant, and reaches the controller through the broker. cacheURL, when
// set, is the cache the grant opens, which holds the node's artifacts unless
// the environment names another store.
func runNodeChild(
	ctx context.Context,
	binary, dir, controllerURL, logsURL, token, cacheURL, cacheGrant, runID, nodeID string,
	logger *slog.Logger,
) (runner.Result, error) {
	fence, _ := store.NodeClaimFenceFromContext(ctx)
	artifact, err := supervisorArtifactStore(ctx, cacheURL, cacheGrant)
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
	if cacheGrant != "" {
		childEnv = append(childEnv, authwire.CacheGrantEnv+"="+cacheGrant)
	}

	// #nosec G702 -- the node runner binary this process resolved, run as argv without a shell
	cmd := exec.Command(binary, "run-node", runID, nodeID)
	cmd.Dir = dir
	cmd.Env = childEnv
	cmd.Stdin = strings.NewReader(broker.capability + "\n")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	outcome, startErr := runAssistedChildProcess(ctx, cmd, logger)
	broker.sealChildLogs(ctx, outcome, startErr, logger)
	if startErr != nil {
		logger.Warn("runNodeRemote: child failed to start",
			"run_id", runID, "node_id", nodeID, "err", startErr)
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("child run-node: %w", startErr),
		}, nil
	}
	if outcome.cancelCause != nil {
		logger.Warn("runNodeRemote: child canceled after owned process cleanup",
			"run_id", runID, "node_id", nodeID,
			"ownership_boundary", assistedChildOwnershipBoundary)
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("child run-node canceled: %w", outcome.cancelCause),
		}, nil
	}
	if outcome.waitErr != nil {
		logger.Warn("runNodeRemote: child exited non-zero",
			"run_id", runID, "node_id", nodeID, "err", outcome.waitErr)
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("child run-node: %w", outcome.waitErr),
		}, nil
	}
	logger.Debug("runNodeRemote: child ownership released",
		"run_id", runID, "node_id", nodeID,
		"ownership_boundary", assistedChildOwnershipBoundary)
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
	"SPARKWING_RUN_HANDLE_FILE":          true,
	"SPARKWING_ONLY":                     true,
	ArtifactStoreEnvVar:                  true,
	"SPARKWING_CACHE_TOKEN":              true,
	authwire.CacheGrantEnv:               true,
	remoteExecutionCapabilityEnv:         true,
	remoteExecutionCapabilityInputEnv:    true,
	remoteBrokeredArtifactEnv:            true,
	remoteBrokeredClaimEnv:               true,
	"SPARKWING_NODE_CLAIM_HOLDER":        true,
	"SPARKWING_NODE_CLAIM_LEASE_SECONDS": true,
	"SPARKWING_NODE_CLAIM_GENERATION":    true,
	"SPARKWING_NODE_CLAIM_MEMBERSHIP":    true,
	"SPARKWING_NODE_CLAIM_RESERVATION":   true,
	"SPARKWING_TRIGGER_CLAIM_GENERATION": true,
	"SPARKWING_TRIGGER_GENERATION":       true,
	"SPARKWING_ATTEMPT_ORDINAL":          true,
}

// ErrRepoNotAllowed marks a run whose repository this machine's owner did not
// allow it to build.
var ErrRepoNotAllowed = errors.New("repository not allowed on this machine")

// AdmitTriggerSource refuses trigger unless allow admits every repository it
// names: the remote fetchURL this machine would fetch, after any ssh-to-https
// rewrite, and the repository its GitHub fields name. A runner that fetches
// with its own credentials runs what it fetches as its own user, so the machine
// owner's list, not the controller, decides what it builds.
func AdmitTriggerSource(allow sourceurl.RepoAllowlist, trigger *store.Trigger, fetchURL string) error {
	named, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil {
		return err
	}
	identities := []string{named}
	if fetchURL != "" {
		fetched, err := sourceurl.Identity(fetchURL)
		if err != nil {
			return err
		}
		identities = append(identities, fetched)
	}
	for _, id := range identities {
		if id != "" && !allow.Admits(id) {
			return fmt.Errorf("%w: run %s builds %s, which this machine's --allow-repo list (%s) does not name; "+
				"trigger it again for a runner whose list names it", ErrRepoNotAllowed, trigger.ID, id, allow)
		}
	}
	return nil
}

// TriggerSourceURL is the remote a trigger's source comes from; direct reports
// a runner that fetches it itself rather than through the git cache.
func TriggerSourceURL(trigger *store.Trigger, direct bool) (string, error) {
	if trigger == nil {
		return "", nil
	}
	return bincache.TriggerRepoURL(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo, direct)
}

func triggerGitHubRepository(trigger *store.Trigger) string {
	if repo := trigger.TriggerEnv["GITHUB_REPOSITORY"]; repo != "" {
		return repo
	}
	if trigger.GithubOwner != "" && trigger.GithubRepo != "" {
		return trigger.GithubOwner + "/" + trigger.GithubRepo
	}
	return ""
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

func resolveRemoteBinary(ctx context.Context, sparkwingDir, controllerURL, controllerToken, gcURL, cacheGrant string, logger *slog.Logger) (remoteBinary, error) {
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		tmp := filepath.Join(sparkwingDir, ".sparkwing-runner-bin")
		if cerr := bincache.CompilePipeline(ctx, sparkwingDir, tmp); cerr != nil {
			return remoteBinary{}, cerr
		}
		return remoteBinary{path: tmp}, nil
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return remoteBinary{}, err
	}
	compiled := false
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		if gcURL != "" || cacheGrant != "" {
			if fetchErr := bincache.TryBinaryPreferSigned(ctx, controllerURL, controllerToken, cacheGrant, gcURL, key, tempPath); fetchErr == nil {
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				logger.Warn("runNodeRemote: bin cache fetch failed; compiling", "err", fetchErr, "hash", key)
			}
		}
		compiled = true
		return bincache.CompilePipeline(ctx, sparkwingDir, tempPath)
	})
	if err != nil {
		return remoteBinary{}, err
	}
	if published && compiled && gcURL != "" {
		if err := bincache.UploadBinary(ctx, gcURL, cacheGrant, key, lease.Path()); err != nil {
			logger.Warn("runNodeRemote: bin cache upload failed", "err", err, "hash", key)
		}
	}
	return remoteBinary{path: lease.Path(), lease: lease}, nil
}
