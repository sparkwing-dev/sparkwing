package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/directdata"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwingcache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func shouldRunRemote(trigger *store.Trigger) bool {
	if trigger == nil {
		return false
	}
	return strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") || triggerGitHubRepository(trigger) != "" || trigger.RepoURL != ""
}

func runNodeRemote(
	ctx context.Context,
	trigger *store.Trigger,
	run *store.Run,
	controllerURL, gitcacheURL, cacheGrant, runID, nodeID, token string,
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
	workspaceSource := strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@")
	if repoURL == "" && !workspaceSource {
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
	case workspaceSource:
		sparkwingDir, err = bincache.FetchSourceBundleDirect(ctx, controllerURL, cacheGrant, runID,
			trigger.TriggerEnv[bincache.SourceBundleObjectEnvKey], trigger.GitSHA, repoURL, workDir)
	case direct:
		sparkwingDir, err = bincache.FetchRunSourceDirect(ctx, bincache.RunSource{
			ControllerURL: controllerURL, ClaimToken: token, RunID: runID,
			RepoURL: repoURL, Branch: branch, SHA: trigger.GitSHA, WorkDir: workDir,
			OwnerCredentials: ownerFenced,
		}, logger)
	default:
		sparkwingDir, err = bincache.FetchPipelineSourceWithCredentials(ctx, gcURL, controllerURL, token, cacheGrant,
			repoURL, branch, trigger.GitSHA, workDir)
	}
	if err != nil {
		return runner.Result{}, fmt.Errorf("fetch source: %w", err)
	}
	if workspaceSource && repoURL != "" {
		adoptNodeBaseline(ctx, trigger, filepath.Dir(sparkwingDir),
			gcURL, bincache.GitcacheBearer(gcURL, controllerURL, token, cacheGrant), runID, nodeID, logger)
	}

	binaryCacheURL := gcURL
	if cacheGrant == "" || bincache.ControllerGitcacheToken(gcURL, controllerURL, token) != "" {
		binaryCacheURL = ""
	}
	binary, err := resolveRemoteBinary(ctx, sparkwingDir, controllerURL, token, runID, binaryCacheURL, cacheGrant, logger)
	if err != nil {
		return runner.Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	defer binary.release()
	logger.Info("runNodeRemote: binary ready",
		"run_id", runID, "node_id", nodeID, "entry", binary.entry)
	art, err := supervisorArtifactStore(ctx, controllerURL, runID, binaryCacheURL, cacheGrant)
	if err != nil {
		return runner.Result{}, fmt.Errorf("open the artifact store: %w", err)
	}
	return hostDispatchedNode(ctx, podNode{
		entry: binary.entry, dir: filepath.Dir(sparkwingDir), controllerURL: controllerURL, token: token,
		runID: runID, nodeID: nodeID, cacheGrant: cacheGrant, artifacts: art, logger: logger,
	})
}

// safety: a host stopped with no cause of its own, as a runner shutting down
// is, leaves its node and run rows to their claims' leases, so the controller
// hands them to another runner rather than record them cancelled or failed; a
// run's cancel, a lost claim or a timeout carries a cause and is recorded.
type stoppingHostState struct {
	*client.Client
	shuttingDown func() bool
}

// safety: no cause of its own is how a runner shutting down stops a host; every other stop names its reason.
func shuttingDownOn(ctx context.Context) func() bool {
	return func() bool { return errors.Is(context.Cause(ctx), context.Canceled) }
}

func (s stoppingHostState) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	if s.shuttingDown() {
		return nil
	}
	return s.Client.FinishRun(ctx, runID, status, errMsg)
}

func (s stoppingHostState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if s.shuttingDown() {
		return nil
	}
	return s.Client.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

// safety: the node runs under the claim the dispatcher or this runner took,
// whose fence ctx carries, so every row and log write the host makes is
// checked against it and the node's terminal row is the result the
// dispatcher reads.
func hostDispatchedNode(ctx context.Context, p podNode) (runner.Result, error) {
	p.dispatched = true
	p.state = stoppingHostState{
		Client:       client.NewWithToken(p.controllerURL, nil, p.token),
		shuttingDown: shuttingDownOn(ctx),
	}
	res, err := hostPodNode(ctx, p)
	if err != nil {
		return runner.Result{}, err
	}
	return runner.Result{Outcome: res.Outcome, Err: res.Err}, nil
}

// safety: a pooled agent serves many runs from one process, so the cache grant is the node's,
// never one read from the process environment.
func supervisorArtifactStore(ctx context.Context, controllerURL, runID, cacheURL, cacheGrant string) (storage.ArtifactStore, error) {
	if os.Getenv(ArtifactStoreEnvVar) != "" {
		return resolveArtifactStoreFromEnv(ctx)
	}
	var legacy storage.ArtifactStore
	if cacheURL != "" && cacheGrant != "" {
		legacy = sparkwingcache.New(cacheURL, cacheGrant, nil)
	}
	if controllerURL != "" && cacheGrant != "" && runID != "" {
		d := directdata.New(controllerURL, cacheGrant, runID, nil)
		available, err := d.Available(ctx)
		if err != nil {
			return nil, err
		}
		if available {
			return directArtifactStore{client: d, legacy: legacy}, nil
		}
	}
	if legacy != nil {
		return legacy, nil
	}
	return resolveArtifactStoreFromEnv(ctx)
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

// ErrRepoNotAllowed marks a run whose repository this machine's owner did not
// allow it to build.
var ErrRepoNotAllowed = errors.New("repository not allowed on this machine")

// AdmitTriggerSource refuses trigger unless allow admits every repository it
// names: the remote fetchURL this machine would fetch, after any ssh-to-https
// rewrite, and the repository its GitHub fields name. A runner that fetches
// with its own credentials runs what it fetches as its own user, so the machine
// owner's list, not the controller, decides what it builds. A trigger naming
// no repository is refused, and so is a working-tree snapshot unless allow
// accepts them: the controller supplies a snapshot's code, which no
// repository on the list holds.
func AdmitTriggerSource(allow sourceurl.RepoAllowlist, trigger *store.Trigger, fetchURL string) error {
	named, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil {
		return err
	}
	if named == "" && fetchURL == "" {
		return fmt.Errorf("%w: run %s names no repository, so this machine's --allow-repo list (%s) cannot admit it",
			ErrRepoNotAllowed, trigger.ID, allow)
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
	if strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") && !allow.AcceptsWorkingTree() {
		return fmt.Errorf("%w: run %s builds a working-tree snapshot whose code the controller supplied, which "+
			"this machine's --allow-repo list cannot vouch for; set accept_working_tree: true in the agent "+
			"section (or pass --accept-working-tree) to build snapshots of the listed repositories",
			ErrRepoNotAllowed, trigger.ID)
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
	entry pipelineentry.Command
	lease *bincache.Lease
}

func (b remoteBinary) release() {
	if b.lease != nil {
		_ = b.lease.Release()
	}
}

func resolveRemoteBinary(ctx context.Context, sparkwingDir, controllerURL, controllerToken, runID, gcURL, token string, logger *slog.Logger) (remoteBinary, error) {
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return remoteBinary{}, err
	}
	if ts {
		cmd, err := PrepareTypeScriptOnRunner(ctx, sparkwingDir)
		return remoteBinary{entry: cmd}, err
	}
	key, err := bincache.PipelineCacheKey(sparkwingDir)
	if err != nil {
		tmp := filepath.Join(sparkwingDir, ".sparkwing-runner-bin")
		if cerr := bincache.CompilePipeline(ctx, sparkwingDir, tmp, bincache.CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr}); cerr != nil {
			return remoteBinary{}, cerr
		}
		return remoteBinary{entry: pipelineentry.Binary(tmp)}, nil
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		return remoteBinary{}, err
	}
	compiled := false
	lease, published, err := entry.AcquireOrMaterialize(ctx, func(tempPath string) error {
		if gcURL != "" || token != "" {
			if fetchErr := bincache.TryBinaryPreferred(ctx, controllerURL, controllerToken, token, runID, gcURL, key, tempPath); fetchErr == nil {
				return nil
			} else if !errors.Is(fetchErr, bincache.ErrMiss) {
				logger.Warn("runNodeRemote: bin cache fetch failed; compiling", "err", fetchErr, "hash", key)
			}
		}
		compiled = true
		return bincache.CompilePipeline(ctx, sparkwingDir, tempPath, bincache.CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr})
	})
	if err != nil {
		return remoteBinary{}, err
	}
	if published && compiled && token != "" {
		if err := bincache.UploadBinaryPreferred(ctx, controllerURL, token, runID, gcURL, key, lease.Path()); err != nil {
			logger.Warn("runNodeRemote: bin cache upload failed", "err", err, "hash", key)
		}
	}
	return remoteBinary{entry: pipelineentry.Binary(lease.Path()), lease: lease}, nil
}
