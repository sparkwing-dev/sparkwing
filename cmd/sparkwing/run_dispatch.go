package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/directdata"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
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
	parseErr                   error
	unknownRunnerFlag          string
	ref                        string
	publish, cacheFrom, target string
	pauseBefore, pauseAfter    []string
	pauseOnFailure             *bool
	runner                     string

	profile  string
	noUpdate bool
	verbose  bool

	noBincache      bool
	hashAllFiles    bool
	allowUnadmitted bool

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

	allowSecretFiles []string

	hereReason string

	index string

	runHandleFile string

	detached           bool
	pipelineRef        string
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

func collectPipelineArgs(passthrough []string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(passthrough) {
		argument := passthrough[i]
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
	inv, err := clisurface.Parse("sparkwing", append([]string{"run", "pipeline"}, args...))
	if err != nil {
		return runFlags{parseErr: err}, nil
	}
	output := runOutput(inv, os.Getenv("SPARKWING_LOG_FORMAT"), color.IsInteractiveStdout())
	if output != "pretty" && output != "json" && output != "plain" {
		return runFlags{parseErr: errors.New("SPARKWING_LOG_FORMAT must be pretty, json or plain")}, nil
	}
	workers, _ := strconv.Atoi(inv.Value("sw-workers"))
	var pause *bool
	if inv.Has("sw-pause-on-failure") {
		value := inv.Bool("sw-pause-on-failure")
		pause = &value
	}
	return runFlags{
		publish: inv.Value("publish"), cacheFrom: inv.Value("sw-cache-from"), target: inv.Value("target"),
		pauseBefore: inv.Flags["sw-pause-before"], pauseAfter: inv.Flags["sw-pause-after"], pauseOnFailure: pause,
		ref: inv.Value("sw-ref"), pipelineRef: inv.Value("sw-pipeline-ref"),
		noUpdate: inv.Bool("sw-no-update"), verbose: inv.Bool("sw-verbose"),
		noBincache: inv.Bool("sw-no-bincache"), hashAllFiles: inv.Bool("sw-hash-all-files"),
		allowUnadmitted: inv.Bool("sw-allow-unadmitted"), workers: workers,
		priority: inv.Value("sw-priority"), prioritySet: inv.Has("sw-priority"),
		startAt: inv.Value("sw-start-at"), stopAt: inv.Value("sw-stop-at"),
		only: inv.Value("sw-only"), noCache: inv.Bool("sw-no-memo"),
		dryRun: inv.Bool("sw-dry-run"), allow: inv.Flags["sw-allow-risk"],
		index: inv.Value("sw-index"), runHandleFile: inv.Value("sw-run-handle-file"),
		detached: inv.Bool("sw-detached"), idempotencyKey: inv.Value("sw-idempotency-key"),
		requestID: inv.Value("sw-request-id"), outputFormat: output,
	}, inv.Args
}

func runOutput(inv clisurface.Invocation, environment string, terminal bool) string {
	if inv.Has("sw-output") {
		return inv.Value("sw-output")
	}
	if inv.Has("output") {
		return inv.Value("output")
	}
	if environment != "" {
		return environment
	}
	return inv.Output(terminal)
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

	// #nosec G702 -- the generated directory and resolved commit ID follow an explicit --.
	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", "--", temporaryDir, string(commit)).CombinedOutput()
	if err != nil {
		if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
			slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
		}
		return "", "", nil, fmt.Errorf("git worktree add %s: %w: %s",
			ref, err, strings.TrimSpace(string(out)))
	}

	pipelineDirectory = filepath.Join(temporaryDir, ".sparkwing")
	if fi, statErr := os.Stat(pipelineDirectory); statErr != nil || !fi.IsDir() {
		if cleanupErr := exec.Command("git", "-C", repoRoot,
			"worktree", "remove", "--force", "--", temporaryDir).Run(); cleanupErr != nil {
			slog.Warn("could not remove temporary Git worktree", "path", temporaryDir, "error", cleanupErr)
		}
		if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
			slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
		}
		return "", "", nil, fmt.Errorf("ref %s has no .sparkwing/ directory", ref)
	}

	// safety: more than one caller runs this, and the second `worktree remove`
	// of a gone path fails loudly.
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			// safety: Windows holds an open handle on a process's working
			// directory, and the exec path leaves it inside the worktree being
			// removed.
			if wd, wdErr := os.Getwd(); wdErr == nil {
				if rel, relErr := filepath.Rel(temporaryDir, wd); relErr == nil && !strings.HasPrefix(rel, "..") {
					if chdirErr := os.Chdir(repoRoot); chdirErr != nil {
						slog.Warn("could not leave the temporary worktree", "path", repoRoot, "error", chdirErr)
					}
				}
			}
			if cleanupErr := exec.Command("git", "-C", repoRoot,
				"worktree", "remove", "--force", "--", temporaryDir).Run(); cleanupErr != nil {
				slog.Warn("could not remove temporary Git worktree", "path", temporaryDir, "error", cleanupErr)
			}
			if cleanupErr := os.RemoveAll(temporaryDir); cleanupErr != nil {
				slog.Warn("could not remove temporary worktree directory", "path", temporaryDir, "error", cleanupErr)
			}
			// safety: git keeps the registration under the origin repository, where a
			// leftover one blocks adding the same path again.
			if err := exec.Command("git", "-C", repoRoot, "worktree", "prune").Run(); err != nil {
				slog.Default().Debug("ref worktree prune did not apply", "repo", repoRoot, "error", err)
			}
		})
	}
	return temporaryDir, pipelineDirectory, cleanup, nil
}

func triggerSource(prefix string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return prefix + "@" + host
	}
	return prefix
}

func createRemoteTrigger(runProfile *namedProfile, pipelineName, source string, flags runFlags, passthrough []string, workingTree bool) (*client.TriggerResponse, error) {
	args := collectPipelineArgs(passthrough)
	branch, sha, repositorySlug, repoURL := detectRemoteGit()
	if repoURL == "" && !workingTree {
		return nil, fmt.Errorf("pipeline trigger %q: no git origin detected from cwd. "+
			"The cluster runner needs a repository URL to clone the pipeline source. "+
			"Run from inside a checkout with an origin remote", pipelineName)
	}
	if repositorySlug != "" {
		repoURL = bincache.RepoURLFromGitHub(repositorySlug)
	} else if repoURL != "" {
		var err error
		repoURL, err = sourceurl.ValidateCloneURL(repoURL)
		if err != nil {
			return nil, fmt.Errorf("pipeline trigger %q: invalid git origin: %w", pipelineName, err)
		}
	}
	var snapshot *worktreeSnapshot
	if workingTree {
		var err error
		snapshot, err = captureWorktreeSnapshot(context.Background(), ".", flags.allowSecretFiles)
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
	if snapshot != nil {
		for key, value := range snapshot.Baseline.Env() {
			environmentValues[key] = value
		}
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
	request.Runner, request.Reason = flags.runner, flags.hereReason

	if snapshot != nil {
		key, uploadErr := uploadSourceBundle(runProfile, snapshot)
		if uploadErr != nil {
			return nil, fmt.Errorf("pipeline trigger %q: upload working-tree snapshot: %w", pipelineName, uploadErr)
		}
		request.Trigger.Env[bincache.SourceBundleObjectEnvKey] = key
		fmt.Fprintf(os.Stderr, "working tree: base %s snapshot %s (%d files, %s)\n",
			snapshot.BaseSHA, snapshot.SHA, snapshot.FileCount, snapshotBytes(snapshot.Size))
		if snapshot.Baseline.SHA != "" {
			fmt.Fprintf(os.Stderr, "working tree: sent baseline %s at %s with the trigger\n",
				snapshot.Baseline.Ref, snapshot.Baseline.SHA)
		}
	} else if repoURL != "" {
		repoDir, cwdErr := os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("pipeline trigger %q: %w", pipelineName, cwdErr)
		}
		if err := offerTriggerSource(runProfile, repoDir, repoURL, sha); err != nil {
			return nil, fmt.Errorf("pipeline trigger %q: %w", pipelineName, err)
		}
	}

	c := client.NewWithToken(runProfile.ControllerURL(), nil, runProfile.ControllerToken())
	response, err := c.CreateTrigger(context.Background(), request)
	if err != nil {
		return nil, fmt.Errorf("create trigger on %s: %w", runProfile.Name, err)
	}
	return response, nil
}

func uploadSourceBundle(runProfile *namedProfile, snapshot *worktreeSnapshot) (string, error) {
	f, err := os.Open(snapshot.BundlePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	key := "sources/" + digest + "/" + hex.EncodeToString(nonce[:])
	transport := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	data := directdata.New(runProfile.ControllerURL(), runProfile.ControllerToken(), "", transport)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	available, err := data.SourceAvailable(ctx)
	if err != nil {
		return "", fmt.Errorf("check direct source support: %w", err)
	}
	if !available {
		return "", errors.New("controller does not support direct source bundles; upgrade it before --working-tree")
	}
	if err := data.Upload(ctx, "source", key, f, snapshot.BundleSize, digest); err != nil {
		return "", err
	}
	return key, nil
}

// safety: a durable remote fetch cannot depend on a cache seed from this shell;
// working-tree submissions upload a separately bound source bundle.
func offerTriggerSource(_ *namedProfile, repoDir, _, sha string) error {
	if !commitOnOrigin(repoDir, sha) {
		return fmt.Errorf("commit %s is not on origin: push your commit or trigger with --working-tree", shortSHA(sha))
	}
	return nil
}

func commitOnOrigin(repoDir, sha string) bool {
	if sha == "" {
		return false
	}
	out, err := exec.Command("git", "-C", repoDir, "for-each-ref", "--count=1", "--contains", sha,
		"--format=%(refname)", "refs/remotes/origin/").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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
