package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func atoiNonNeg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("must be >= 0, got %d", n)
	}
	return n, nil
}

type runFlags struct {
	unknownRunnerFlag string
	ref               string

	profile  string
	noUpdate bool
	verbose  bool

	secrets   string
	changeDir string

	mode string

	workers int

	priority    string
	prioritySet bool

	startAt string
	stopAt  string

	only string

	noCache bool

	dryRun bool

	allow []string

	localOnly bool
	fleet     bool

	index string

	runHandleFile string

	isolatedHome string

	detached           bool
	idempotencyKey     string
	requestID          string
	consumerIdle       string
	consumerClaimLease string
	outputFormat       string
}

type detachedOnlyFlag struct {
	name  string
	value string
}

func (flags runFlags) detachedOnlyFlags() []detachedOnlyFlag {
	return []detachedOnlyFlag{
		{"--sw-idempotency-key", flags.idempotencyKey},
		{"--sw-request-id", flags.requestID},
		{"--sw-consumer-idle", flags.consumerIdle},
		{"--sw-consumer-claim-lease", flags.consumerClaimLease},
		{"--sw-output", flags.outputFormat},
	}
}

func validatePriorityFlag(v string) (string, error) {
	s := strings.TrimSpace(v)
	switch s {
	case wingwire.PriorityFront, wingwire.PriorityBack:
		return s, nil
	}
	if _, err := strconv.Atoi(s); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("--sw-priority %q: expected an integer, %q, or %q",
		v, wingwire.PriorityFront, wingwire.PriorityBack)
}

func isolatedHomeConfigDir(root string) string { return filepath.Join(root, "config") }

// safety: daemon startup and toolchain replacement read the parent environment.
func applyIsolatedHome(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("--sw-isolated-home %s: %w", dir, err)
	}
	if err := fssecure.EnsureDir(abs); err != nil {
		return fmt.Errorf("--sw-isolated-home %s: %w", dir, err)
	}
	config := isolatedHomeConfigDir(abs)
	if err := fssecure.EnsureDir(config); err != nil {
		return fmt.Errorf("--sw-isolated-home %s: %w", dir, err)
	}
	if err := os.Setenv("SPARKWING_HOME", abs); err != nil {
		return fmt.Errorf("--sw-isolated-home %s: %w", dir, err)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", config); err != nil {
		return fmt.Errorf("--sw-isolated-home %s: %w", dir, err)
	}
	return nil
}

func collectPipelineArgs(passthrough []string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(passthrough) {
		argument := passthrough[i]
		// safety: the separator has no argument name to record.
		if argument == "--" {
			i++
			continue
		}
		if !strings.HasPrefix(argument, "--") {
			i++
			continue
		}
		name := strings.TrimPrefix(argument, "--")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			key := name[:eq]
			if key != "" {
				out[key] = name[eq+1:]
			}
			i++
			continue
		}
		if i+1 < len(passthrough) && !strings.HasPrefix(passthrough[i+1], "--") {
			out[name] = passthrough[i+1]
			i += 2
			continue
		}
		out[name] = "true"
		i++
	}
	return out
}

func appendCSV(out []string, v string) []string {
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseRunFlags(args []string) (runFlags, []string) {
	var flags runFlags
	passthroughArgs := make([]string, 0, len(args))
	argumentIndex := 0
	for argumentIndex < len(args) {
		argument := args[argumentIndex]
		if argument == "--" {
			return flags, append(passthroughArgs, args[argumentIndex:]...)
		}
		switch {
		case argument == "--sw-ref":
			if argumentIndex+1 < len(args) {
				flags.ref = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-ref="):
			flags.ref = strings.TrimPrefix(argument, "--sw-ref=")
			argumentIndex++
		case argument == "--profile":
			if argumentIndex+1 < len(args) {
				flags.profile = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--profile="):
			flags.profile = strings.TrimPrefix(argument, "--profile=")
			argumentIndex++
		case argument == "--sw-no-update":
			flags.noUpdate = true
			argumentIndex++
		case argument == "--sw-verbose", argument == "-v":
			flags.verbose = true
			argumentIndex++
		case argument == "--sw-secrets":
			if argumentIndex+1 < len(args) {
				flags.secrets = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-secrets="):
			flags.secrets = strings.TrimPrefix(argument, "--sw-secrets=")
			argumentIndex++
		case argument == "--sw-mode":
			if argumentIndex+1 < len(args) {
				flags.mode = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-mode="):
			flags.mode = strings.TrimPrefix(argument, "--sw-mode=")
			argumentIndex++
		case argument == "--sw-workers":
			if argumentIndex+1 < len(args) {
				if n, err := atoiNonNeg(args[argumentIndex+1]); err == nil {
					flags.workers = n
					argumentIndex += 2
					continue
				}
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-workers="):
			if n, err := atoiNonNeg(strings.TrimPrefix(argument, "--sw-workers=")); err == nil {
				flags.workers = n
			}
			argumentIndex++
		// safety: invalid priorities must fail before the run enters the queue.
		case argument == "--sw-priority":
			flags.prioritySet = true
			if argumentIndex+1 < len(args) {
				flags.priority = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-priority="):
			flags.priority = strings.TrimPrefix(argument, "--sw-priority=")
			flags.prioritySet = true
			argumentIndex++
		case argument == "--sw-start-at":
			if argumentIndex+1 < len(args) {
				flags.startAt = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-start-at="):
			flags.startAt = strings.TrimPrefix(argument, "--sw-start-at=")
			argumentIndex++
		case argument == "--sw-stop-at":
			if argumentIndex+1 < len(args) {
				flags.stopAt = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-stop-at="):
			flags.stopAt = strings.TrimPrefix(argument, "--sw-stop-at=")
			argumentIndex++
		case argument == "--sw-only":
			if argumentIndex+1 < len(args) {
				flags.only = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-only="):
			flags.only = strings.TrimPrefix(argument, "--sw-only=")
			argumentIndex++
		case argument == "--sw-no-cache":
			flags.noCache = true
			argumentIndex++
		case argument == "--sw-local-only":
			flags.localOnly = true
			argumentIndex++
		case argument == "--sw-fleet":
			flags.fleet = true
			argumentIndex++
		case argument == "--sw-dry-run", argument == "--dry-run=true":
			flags.dryRun = true
			argumentIndex++
		case argument == "--dry-run=false":
			flags.dryRun = false
			argumentIndex++
		case argument == "--sw-allow":
			if argumentIndex+1 < len(args) {
				flags.allow = appendCSV(flags.allow, args[argumentIndex+1])
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-allow="):
			flags.allow = appendCSV(flags.allow, strings.TrimPrefix(argument, "--sw-allow="))
			argumentIndex++
		case argument == "--sw-index":
			if argumentIndex+1 < len(args) {
				flags.index = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-index="):
			flags.index = strings.TrimPrefix(argument, "--sw-index=")
			argumentIndex++
		case argument == "--sw-run-handle-file":
			if argumentIndex+1 < len(args) {
				flags.runHandleFile = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-run-handle-file="):
			flags.runHandleFile = strings.TrimPrefix(argument, "--sw-run-handle-file=")
			argumentIndex++
		case argument == "--sw-isolated-home":
			if argumentIndex+1 < len(args) {
				flags.isolatedHome = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-isolated-home="):
			flags.isolatedHome = strings.TrimPrefix(argument, "--sw-isolated-home=")
			argumentIndex++
		case argument == "--sw-detached":
			flags.detached = true
			argumentIndex++
		case argument == "--sw-idempotency-key":
			if argumentIndex+1 < len(args) {
				flags.idempotencyKey = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-idempotency-key="):
			flags.idempotencyKey = strings.TrimPrefix(argument, "--sw-idempotency-key=")
			argumentIndex++
		case argument == "--sw-request-id":
			if argumentIndex+1 < len(args) {
				flags.requestID = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-request-id="):
			flags.requestID = strings.TrimPrefix(argument, "--sw-request-id=")
			argumentIndex++
		case argument == "--sw-consumer-idle":
			if argumentIndex+1 < len(args) {
				flags.consumerIdle = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-consumer-idle="):
			flags.consumerIdle = strings.TrimPrefix(argument, "--sw-consumer-idle=")
			argumentIndex++
		case argument == "--sw-consumer-claim-lease":
			if argumentIndex+1 < len(args) {
				flags.consumerClaimLease = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-consumer-claim-lease="):
			flags.consumerClaimLease = strings.TrimPrefix(argument, "--sw-consumer-claim-lease=")
			argumentIndex++
		case argument == "--sw-output":
			if argumentIndex+1 < len(args) {
				flags.outputFormat = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-output="):
			flags.outputFormat = strings.TrimPrefix(argument, "--sw-output=")
			argumentIndex++
		case argument == "-C", argument == "--sw-cd":
			if argumentIndex+1 < len(args) {
				flags.changeDir = args[argumentIndex+1]
				argumentIndex += 2
				continue
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		case strings.HasPrefix(argument, "--sw-cd="):
			flags.changeDir = strings.TrimPrefix(argument, "--sw-cd=")
			argumentIndex++
		default:
			if strings.HasPrefix(argument, "--sw-") && flags.unknownRunnerFlag == "" {
				flags.unknownRunnerFlag = argument
			}
			passthroughArgs = append(passthroughArgs, argument)
			argumentIndex++
		}
	}
	return flags, passthroughArgs
}

const EventIndexBound = "index_bound"

const (
	logFormatJSON   = "json"
	logFormatPretty = "pretty"
)

func bindRunIndex(env []string, path string, out io.Writer, logFormat string) ([]string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("--sw-index %s: %w", path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("--sw-index %s: %w", path, err)
	}
	if err := announceIndexBound(out, abs, logFormat); err != nil {
		return nil, fmt.Errorf("--sw-index %s: announce binding: %w", path, err)
	}
	return setEnv(env, "GIT_INDEX_FILE", abs), nil
}

func announceIndexBound(out io.Writer, abs, logFormat string) error {
	if logFormat != logFormatJSON {
		_, err := fmt.Fprintf(out, "index bound: %s\n", abs)
		return err
	}
	record := sparkwing.LogRecord{
		TS:    time.Now(),
		Event: EventIndexBound,
		Attrs: map[string]any{"path": abs},
	}
	return json.NewEncoder(out).Encode(&record)
}

func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, pipelineDirectory string, cleanup func(), err error) {
	repoRoot := filepath.Dir(sparkwingDir)

	commit, err := orchestrator.ResolveRefCommit(context.Background(), repoRoot, ref, slog.Default())
	if err != nil {
		return "", "", nil, err
	}

	temporaryDir, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", "--", temporaryDir, string(commit)).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(temporaryDir)
		return "", "", nil, fmt.Errorf("git worktree add %s: %w: %s",
			ref, err, strings.TrimSpace(string(out)))
	}

	pipelineDirectory = filepath.Join(temporaryDir, ".sparkwing")
	if fi, statErr := os.Stat(pipelineDirectory); statErr != nil || !fi.IsDir() {
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", temporaryDir).Run()
		_ = os.RemoveAll(temporaryDir)
		return "", "", nil, fmt.Errorf("ref %s has no .sparkwing/ directory", ref)
	}

	cleanup = func() {
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", "--", temporaryDir).Run()
		_ = os.RemoveAll(temporaryDir)
		// safety: git keeps the registration under the origin repository, where a
		// leftover one blocks adding the same path again.
		if err := exec.Command("git", "-C", repoRoot, "worktree", "prune").Run(); err != nil {
			slog.Default().Debug("ref worktree prune did not apply", "repo", repoRoot, "error", err)
		}
	}
	return temporaryDir, pipelineDirectory, cleanup, nil
}

func triggerSource(prefix string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return prefix + "@" + host
	}
	return prefix
}

func createRemoteTrigger(runProfile *profile.Profile, pipelineName, source string, flags runFlags, passthrough []string, workingTree bool) (*client.TriggerResponse, error) {
	args := collectPipelineArgs(passthrough)
	var userName string
	if u, err := user.Current(); err == nil {
		userName = u.Username
	}

	branch, sha, repositorySlug, repoURL := detectRemoteGit()
	if repoURL == "" {
		return nil, fmt.Errorf("pipeline trigger %q: no git origin detected from cwd. "+
			"The cluster runner needs a repository URL to clone the pipeline source. "+
			"Run from inside a checkout with an origin remote", pipelineName)
	}
	if repositorySlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repositorySlug)
	} else {
		var err error
		repoURL, err = sourceurl.ValidateCloneURL(repoURL)
		if err != nil {
			return nil, fmt.Errorf("pipeline trigger %q: invalid git origin: %w", pipelineName, err)
		}
	}
	var snapshot *worktreeSnapshot
	if workingTree {
		var err error
		snapshot, err = captureWorktreeSnapshot(context.Background(), ".")
		if err != nil {
			return nil, fmt.Errorf("pipeline trigger %q: %w", pipelineName, err)
		}
		defer func() { _ = snapshot.close() }()
		sha = snapshot.SHA
	}
	environmentValues := map[string]string{}
	if repositorySlug != "" {
		environmentValues["GITHUB_REPOSITORY"] = repositorySlug
	}
	if flags.startAt != "" {
		environmentValues["SPARKWING_START_AT"] = flags.startAt
	}
	if flags.stopAt != "" {
		environmentValues["SPARKWING_STOP_AT"] = flags.stopAt
	}
	if flags.dryRun {
		environmentValues["SPARKWING_DRY_RUN"] = "1"
	}
	if flags.only != "" {
		environmentValues["SPARKWING_ONLY"] = flags.only
	}
	if flags.noCache {
		environmentValues["SPARKWING_NO_CACHE"] = "1"
	}

	triggerBranch := flags.ref
	if triggerBranch == "" {
		triggerBranch = branch
	}

	owner, githubRepository, name := "", "", ""
	if slash := strings.IndexByte(repositorySlug, '/'); slash > 0 {
		owner, githubRepository = repositorySlug[:slash], repositorySlug[slash+1:]
		name = githubRepository
	} else {
		name = repoNameFromURL(repoURL)
	}

	request := client.TriggerRequest{
		Pipeline: pipelineName,
		Args:     args,
		Trigger: client.TriggerMeta{
			Source: source,
			User:   userName,
			Env:    environmentValues,
		},
		Git: client.GitMeta{
			Branch:      triggerBranch,
			SHA:         sha,
			Repo:        name,
			RepoURL:     repoURL,
			GithubOwner: owner,
			GithubRepo:  githubRepository,
		},
	}

	if snapshot != nil {
		cacheURL := bincache.CacheURL()
		seedErr := seedWorkingTreeSnapshot(runProfile, cacheURL, repoURL, snapshot, 2*time.Minute, 15*time.Minute)
		if seedErr != nil {
			return nil, fmt.Errorf("pipeline trigger %q: upload working-tree snapshot: %w", pipelineName, seedErr)
		}
		fmt.Fprintf(os.Stderr, "working tree: base %s snapshot %s (%d files, %s)\n",
			snapshot.BaseSHA, snapshot.SHA, snapshot.FileCount, snapshotBytes(snapshot.Size))
	} else if repoURL != "" {
		discoveryContext, cancelDiscovery := context.WithTimeout(context.Background(), 5*time.Second)
		services, discoveryErr := discovery.ServicesFor(discoveryContext, runProfile.ControllerURL(), runProfile.ControllerToken())
		cancelDiscovery()
		seedTriggerSource(runProfile, services.CachePod, discoveryErr, repoURL, sha)
	}

	c := client.NewWithToken(runProfile.ControllerURL(), nil, runProfile.ControllerToken())
	response, err := c.CreateTrigger(context.Background(), request)
	if err != nil {
		return nil, fmt.Errorf("create trigger on %s: %w", runProfile.Name, err)
	}
	return response, nil
}

func seedWorkingTreeSnapshot(runProfile *profile.Profile, cacheURL, repoURL string, snapshot *worktreeSnapshot, directTimeout, controllerTimeout time.Duration) error {
	var directErr error
	if cacheURL != "" {
		contextValue, cancel := context.WithTimeout(context.Background(), directTimeout)
		directErr = bincache.SeedWorkspaceBundle(contextValue, cacheURL, bincache.CacheToken(), repoURL, snapshot.BundlePath, snapshot.SHA)
		cancel()
		if directErr == nil {
			return nil
		}
	}
	contextValue, cancel := context.WithTimeout(context.Background(), controllerTimeout)
	controllerErr := bincache.SeedWorkspaceBundleViaController(contextValue, runProfile.ControllerURL(), runProfile.ControllerToken(), repoURL, snapshot.BundlePath, snapshot.SHA)
	cancel()
	if controllerErr == nil {
		return nil
	}
	if directErr != nil {
		return errors.Join(fmt.Errorf("direct cache: %w", directErr), fmt.Errorf("controller proxy: %w", controllerErr))
	}
	return controllerErr
}

func seedTriggerSource(runProfile *profile.Profile, cacheURL string, discoveryErr error, repoURL, sha string) {
	repoDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sparkwing run: gitcache seed skipped (cwd: %v)\n", err)
		return
	}
	if cacheURL != "" {
		refreshContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = bincache.RefreshRepo(refreshContext, cacheURL, bincache.CacheToken(), repoURL)
		cancel()
		if err == nil {
			return
		}
		seedContext, seedCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		seedErr := bincache.SeedRepo(seedContext, cacheURL, bincache.CacheToken(), repoURL, repoDir, sha)
		seedCancel()
		if seedErr == nil {
			return
		}
		fmt.Fprintf(os.Stderr,
			"sparkwing run: gitcache refresh failed (%v), seed failed (%v); continuing; the runner retries if the source commit is unavailable\n",
			err, seedErr)
		return
	}

	refreshContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = bincache.RefreshRepoViaController(refreshContext, runProfile.ControllerURL(), runProfile.ControllerToken(), repoURL)
	cancel()
	if err == nil {
		return
	}
	seedContext, seedCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	seedErr := bincache.SeedRepoViaController(seedContext, runProfile.ControllerURL(), runProfile.ControllerToken(), repoURL, repoDir, sha)
	seedCancel()
	if seedErr == nil {
		return
	}
	if isHTTPNotFound(seedErr) {
		return
	}
	if discoveryErr != nil {
		fmt.Fprintf(os.Stderr,
			"sparkwing run: service discovery failed (%v), controller gitcache refresh failed (%v), seed failed (%v); continuing; the runner retries if the source commit is unavailable\n",
			discoveryErr, err, seedErr)
		return
	}
	fmt.Fprintf(os.Stderr,
		"sparkwing run: controller gitcache refresh failed (%v), seed failed (%v); continuing; the runner retries if the source commit is unavailable\n",
		err, seedErr)
}

func isHTTPNotFound(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "404 ")
}

func detectRemoteGit() (branch, sha, repo, repoURL string) {
	return gitContextIn("")
}

func gitContextIn(dir string) (branch, sha, repo, repoURL string) {
	git := func(args ...string) (string, bool) {
		if dir != "" {
			args = append([]string{"-C", dir}, args...)
		}
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	if v, ok := git("rev-parse", "--abbrev-ref", "HEAD"); ok {
		branch = v
		if branch == "HEAD" {
			branch = ""
		}
	}
	if v, ok := git("rev-parse", "HEAD"); ok {
		sha = v
	}
	if v, ok := git("remote", "get-url", "origin"); ok {
		repoURL = v
		repo = parseGithubOwnerRepo(repoURL)
	}
	return branch, sha, repo, repoURL
}

func parseGithubOwnerRepo(url string) string {
	if strings.HasPrefix(url, "git@github.com:") {
		rest := strings.TrimPrefix(url, "git@github.com:")
		rest = strings.TrimSuffix(rest, ".git")
		return rest
	}
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if strings.HasPrefix(url, prefix) {
			rest := strings.TrimPrefix(url, prefix)
			rest = strings.TrimSuffix(rest, ".git")
			return rest
		}
	}
	return ""
}

func repoNameFromURL(url string) string {
	url = strings.TrimSpace(strings.TrimSuffix(url, ".git"))
	if url == "" {
		return ""
	}
	i := strings.LastIndexAny(url, "/:")
	if i < 0 || i == len(url)-1 {
		return url
	}
	return url[i+1:]
}
