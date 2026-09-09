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
	ref string

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

// detachedOnlyFlag names one flag that only a detached launch reads, so the
// refusal can name it back to the caller.
type detachedOnlyFlag struct {
	name  string
	value string
}

func (wf runFlags) detachedOnlyFlags() []detachedOnlyFlag {
	return []detachedOnlyFlag{
		{"--sw-idempotency-key", wf.idempotencyKey},
		{"--sw-request-id", wf.requestID},
		{"--sw-consumer-idle", wf.consumerIdle},
		{"--sw-consumer-claim-lease", wf.consumerClaimLease},
		{"--sw-output", wf.outputFormat},
	}
}

// validatePriorityFlag accepts the three forms --sw-priority takes and returns
// the value the pipeline program receives verbatim: `front` and `back` resolve
// against the live queue there, an integer is already the answer.
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

// safety: the pre-warmed daemon, the toolchain re-exec, and the compiled
// pipeline binary all read the process environment, so an isolated home has to
// land there rather than only on the child's environment.
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
		tok := passthrough[i]
		// A bare separator ends sparkwing's own flags; recording it would put an
		// empty-named argument on the run.
		if tok == "--" {
			i++
			continue
		}
		if !strings.HasPrefix(tok, "--") {
			i++
			continue
		}
		name := strings.TrimPrefix(tok, "--")
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
	var wf runFlags
	pass := make([]string, 0, len(args))
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--sw-ref":
			if i+1 < len(args) {
				wf.ref = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-ref="):
			wf.ref = strings.TrimPrefix(a, "--sw-ref=")
			i++
		case a == "--profile":
			if i+1 < len(args) {
				wf.profile = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--profile="):
			wf.profile = strings.TrimPrefix(a, "--profile=")
			i++
		case a == "--sw-no-update":
			wf.noUpdate = true
			i++
		case a == "--sw-verbose", a == "-v":
			wf.verbose = true
			i++
		case a == "--sw-secrets":
			if i+1 < len(args) {
				wf.secrets = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-secrets="):
			wf.secrets = strings.TrimPrefix(a, "--sw-secrets=")
			i++
		case a == "--sw-mode":
			if i+1 < len(args) {
				wf.mode = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-mode="):
			wf.mode = strings.TrimPrefix(a, "--sw-mode=")
			i++
		case a == "--sw-workers":
			if i+1 < len(args) {
				if n, err := atoiNonNeg(args[i+1]); err == nil {
					wf.workers = n
					i += 2
					continue
				}
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-workers="):
			if n, err := atoiNonNeg(strings.TrimPrefix(a, "--sw-workers=")); err == nil {
				wf.workers = n
			}
			i++
		// safety: an unusable value is peeled and refused at validation rather
		// than forwarded, because a run that silently kept the author's
		// priority would look admitted in the order the operator asked for.
		case a == "--sw-priority":
			wf.prioritySet = true
			if i+1 < len(args) {
				wf.priority = args[i+1]
				i += 2
				continue
			}
			i++
		case strings.HasPrefix(a, "--sw-priority="):
			wf.priority = strings.TrimPrefix(a, "--sw-priority=")
			wf.prioritySet = true
			i++
		case a == "--sw-start-at":
			if i+1 < len(args) {
				wf.startAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-start-at="):
			wf.startAt = strings.TrimPrefix(a, "--sw-start-at=")
			i++
		case a == "--sw-stop-at":
			if i+1 < len(args) {
				wf.stopAt = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-stop-at="):
			wf.stopAt = strings.TrimPrefix(a, "--sw-stop-at=")
			i++
		case a == "--sw-only":
			if i+1 < len(args) {
				wf.only = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-only="):
			wf.only = strings.TrimPrefix(a, "--sw-only=")
			i++
		case a == "--sw-no-cache":
			wf.noCache = true
			i++
		case a == "--sw-local-only":
			wf.localOnly = true
			i++
		case a == "--sw-fleet":
			wf.fleet = true
			i++
		case a == "--sw-dry-run", a == "--dry-run=true":
			wf.dryRun = true
			i++
		case a == "--dry-run=false":
			wf.dryRun = false
			i++
		case a == "--sw-allow":
			if i+1 < len(args) {
				wf.allow = appendCSV(wf.allow, args[i+1])
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-allow="):
			wf.allow = appendCSV(wf.allow, strings.TrimPrefix(a, "--sw-allow="))
			i++
		case a == "--sw-index":
			if i+1 < len(args) {
				wf.index = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-index="):
			wf.index = strings.TrimPrefix(a, "--sw-index=")
			i++
		case a == "--sw-run-handle-file":
			if i+1 < len(args) {
				wf.runHandleFile = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-run-handle-file="):
			wf.runHandleFile = strings.TrimPrefix(a, "--sw-run-handle-file=")
			i++
		case a == "--sw-isolated-home":
			if i+1 < len(args) {
				wf.isolatedHome = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-isolated-home="):
			wf.isolatedHome = strings.TrimPrefix(a, "--sw-isolated-home=")
			i++
		case a == "--sw-detached":
			wf.detached = true
			i++
		case a == "--sw-idempotency-key":
			if i+1 < len(args) {
				wf.idempotencyKey = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-idempotency-key="):
			wf.idempotencyKey = strings.TrimPrefix(a, "--sw-idempotency-key=")
			i++
		case a == "--sw-request-id":
			if i+1 < len(args) {
				wf.requestID = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-request-id="):
			wf.requestID = strings.TrimPrefix(a, "--sw-request-id=")
			i++
		case a == "--sw-consumer-idle":
			if i+1 < len(args) {
				wf.consumerIdle = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-consumer-idle="):
			wf.consumerIdle = strings.TrimPrefix(a, "--sw-consumer-idle=")
			i++
		case a == "--sw-consumer-claim-lease":
			if i+1 < len(args) {
				wf.consumerClaimLease = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-consumer-claim-lease="):
			wf.consumerClaimLease = strings.TrimPrefix(a, "--sw-consumer-claim-lease=")
			i++
		case a == "--sw-output":
			if i+1 < len(args) {
				wf.outputFormat = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-output="):
			wf.outputFormat = strings.TrimPrefix(a, "--sw-output=")
			i++
		case a == "-C", a == "--sw-cd":
			if i+1 < len(args) {
				wf.changeDir = args[i+1]
				i += 2
				continue
			}
			pass = append(pass, a)
			i++
		case strings.HasPrefix(a, "--sw-cd="):
			wf.changeDir = strings.TrimPrefix(a, "--sw-cd=")
			i++
		default:
			pass = append(pass, a)
			i++
		}
	}
	return wf, pass
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
	rec := sparkwing.LogRecord{
		TS:    time.Now(),
		Event: EventIndexBound,
		Attrs: map[string]any{"path": abs},
	}
	return json.NewEncoder(out).Encode(&rec)
}

func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, sparkwingSub string, cleanup func(), err error) {
	repoRoot := filepath.Dir(sparkwingDir)

	rev, err := orchestrator.ResolveRefCommit(context.Background(), repoRoot, ref, slog.Default())
	if err != nil {
		return "", "", nil, err
	}

	tmpDir, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", "--", tmpDir, string(rev)).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("git worktree add %s: %w: %s",
			ref, err, strings.TrimSpace(string(out)))
	}

	sub := filepath.Join(tmpDir, ".sparkwing")
	if fi, statErr := os.Stat(sub); statErr != nil || !fi.IsDir() {
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", tmpDir).Run()
		_ = os.RemoveAll(tmpDir)
		return "", "", nil, fmt.Errorf("ref %s has no .sparkwing/ directory", ref)
	}

	cleanup = func() {
		_ = exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", "--", tmpDir).Run()
		_ = os.RemoveAll(tmpDir)
		// safety: git keeps the registration under the origin repository, where a
		// leftover one blocks adding the same path again.
		if err := exec.Command("git", "-C", repoRoot, "worktree", "prune").Run(); err != nil {
			slog.Default().Debug("ref worktree prune did not apply", "repo", repoRoot, "error", err)
		}
	}
	return tmpDir, sub, cleanup, nil
}

func triggerSource(prefix string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return prefix + "@" + host
	}
	return prefix
}

func createRemoteTrigger(prof *profile.Profile, pipelineName, source string, wf runFlags, passthrough []string, workingTree bool) (*client.TriggerResponse, error) {
	args := collectPipelineArgs(passthrough)
	var userName string
	if u, err := user.Current(); err == nil {
		userName = u.Username
	}

	branch, sha, repoSlug, repoURL := detectRemoteGit()
	if repoURL == "" {
		return nil, fmt.Errorf("pipeline trigger %q: no git origin detected from cwd. "+
			"The cluster runner needs a repository URL to clone the pipeline source. "+
			"Run from inside a checkout with an origin remote", pipelineName)
	}
	if repoSlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repoSlug)
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
	envMap := map[string]string{}
	if repoSlug != "" {
		envMap["GITHUB_REPOSITORY"] = repoSlug
	}
	if wf.startAt != "" {
		envMap["SPARKWING_START_AT"] = wf.startAt
	}
	if wf.stopAt != "" {
		envMap["SPARKWING_STOP_AT"] = wf.stopAt
	}
	if wf.dryRun {
		envMap["SPARKWING_DRY_RUN"] = "1"
	}
	if wf.only != "" {
		envMap["SPARKWING_ONLY"] = wf.only
	}
	if wf.noCache {
		envMap["SPARKWING_NO_CACHE"] = "1"
	}

	triggerBranch := wf.ref
	if triggerBranch == "" {
		triggerBranch = branch
	}

	owner, githubRepo, name := "", "", ""
	if slash := strings.IndexByte(repoSlug, '/'); slash > 0 {
		owner, githubRepo = repoSlug[:slash], repoSlug[slash+1:]
		name = githubRepo
	} else {
		name = repoNameFromURL(repoURL)
	}

	req := client.TriggerRequest{
		Pipeline: pipelineName,
		Args:     args,
		Trigger: client.TriggerMeta{
			Source: source,
			User:   userName,
			Env:    envMap,
		},
		Git: client.GitMeta{
			Branch:      triggerBranch,
			SHA:         sha,
			Repo:        name,
			RepoURL:     repoURL,
			GithubOwner: owner,
			GithubRepo:  githubRepo,
		},
	}

	if snapshot != nil {
		cacheURL := bincache.CacheURL()
		seedErr := seedWorkingTreeSnapshot(prof, cacheURL, repoURL, snapshot, 2*time.Minute, 15*time.Minute)
		if seedErr != nil {
			return nil, fmt.Errorf("pipeline trigger %q: upload working-tree snapshot: %w", pipelineName, seedErr)
		}
		fmt.Fprintf(os.Stderr, "working tree: base %s snapshot %s (%d files, %s)\n",
			snapshot.BaseSHA, snapshot.SHA, snapshot.FileCount, snapshotBytes(snapshot.Size))
	} else if repoURL != "" {
		discoverCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
		services, derr := discovery.ServicesFor(discoverCtx, prof.ControllerURL(), prof.ControllerToken())
		dCancel()
		repoDir, cwdErr := os.Getwd()
		if cwdErr != nil {
			fmt.Fprintf(os.Stderr, "sparkwing run: gitcache seed skipped (cwd: %v)\n", cwdErr)
		} else {
			seedTriggerSource(prof, services.CachePod, derr, repoDir, repoURL, sha)
		}
	}

	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	resp, err := c.CreateTrigger(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("create trigger on %s: %w", prof.Name, err)
	}
	return resp, nil
}

func seedWorkingTreeSnapshot(prof *profile.Profile, cacheURL, repoURL string, snapshot *worktreeSnapshot, directTimeout, controllerTimeout time.Duration) error {
	var directErr error
	if cacheURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), directTimeout)
		directErr = bincache.SeedWorkspaceBundle(ctx, cacheURL, bincache.CacheToken(), repoURL, snapshot.BundlePath, snapshot.SHA)
		cancel()
		if directErr == nil {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), controllerTimeout)
	controllerErr := bincache.SeedWorkspaceBundleViaController(ctx, prof.ControllerURL(), prof.ControllerToken(), repoURL, snapshot.BundlePath, snapshot.SHA)
	cancel()
	if controllerErr == nil {
		return nil
	}
	if directErr != nil {
		return errors.Join(fmt.Errorf("direct cache: %w", directErr), fmt.Errorf("controller proxy: %w", controllerErr))
	}
	return controllerErr
}

func seedTriggerSource(prof *profile.Profile, cacheURL string, discoveryErr error, repoDir, repoURL, sha string) {
	if cacheURL != "" {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := bincache.RefreshRepo(refreshCtx, cacheURL, bincache.CacheToken(), repoURL)
		cancel()
		if err == nil {
			return
		}
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		seedErr := bincache.SeedRepo(seedCtx, cacheURL, bincache.CacheToken(), repoURL, repoDir, sha)
		seedCancel()
		if seedErr == nil {
			return
		}
		fmt.Fprintf(os.Stderr,
			"sparkwing run: gitcache refresh failed (%v), seed failed (%v); proceeding -- runner will retry on stale-SHA\n",
			err, seedErr)
		return
	}

	refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := bincache.RefreshRepoViaController(refreshCtx, prof.ControllerURL(), prof.ControllerToken(), repoURL)
	cancel()
	if err == nil {
		return
	}
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	seedErr := bincache.SeedRepoViaController(seedCtx, prof.ControllerURL(), prof.ControllerToken(), repoURL, repoDir, sha)
	seedCancel()
	if seedErr == nil {
		return
	}
	if isHTTPNotFound(seedErr) {
		return
	}
	if discoveryErr != nil {
		fmt.Fprintf(os.Stderr,
			"sparkwing run: service discovery failed (%v), controller gitcache refresh failed (%v), seed failed (%v); proceeding -- runner will retry on stale-SHA\n",
			discoveryErr, err, seedErr)
		return
	}
	fmt.Fprintf(os.Stderr,
		"sparkwing run: controller gitcache refresh failed (%v), seed failed (%v); proceeding -- runner will retry on stale-SHA\n",
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
