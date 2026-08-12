// sparkwing run flag parsing. parseRunFlags walks args manually (not
// pflag) because the pipeline binary defines its own flags; we strip
// the sw-prefixed flags we know and pass the rest through untouched.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
	// profile names a storage profile from
	// ~/.config/sparkwing/profiles.yaml for local execution: state,
	// logs, and cache route through the profile (with a local SQLite
	// mirror for non-local profiles). Parsed from the flat --profile
	// flag, forwarded to the inner binary as SPARKWING_PROFILE.
	profile  string
	noUpdate bool
	verbose  bool
	// secrets sources secrets from the named profile's controller.
	// Orthogonal to --profile: --secrets prod resolves against prod even
	// when running locally. Empty = laptop dotenv.
	secrets   string
	changeDir string
	// mode: "" / "local" = in-process workers; "ci-embedded" = capped
	// local procs + S3 storage.
	mode string
	// workers caps concurrent nodes in ci-embedded mode; 0 = NumCPU.
	workers int
	// --start-at / --stop-at name an inclusive WorkStep window the
	// orchestrator runs; ids outside the resulting reachability set
	// are skipped with `step_skipped`. Either bound can be empty to
	// leave that side open. Unknown ids fail the run with a "did you
	// mean X?" suggestion at registration time.
	startAt string
	stopAt  string
	// --only is a job-level filter (path.Match glob over JobNode IDs).
	// Matched jobs run; jobs reachable as transitive Needs() ancestors
	// of matched jobs also run (so a glob hitting only the leaves still
	// produces a self-consistent dispatch). Everything else is skipped
	// with `node_skipped`. Mutually exclusive with --start-at / --stop-at:
	// they're a different filter mode (step-level reachability) and
	// intersecting the two would produce surprising selections.
	only string
	// --no-cache disables cache READS for this run; per-node cache
	// WRITES still happen on success so subsequent runs over the same
	// content hit cache normally. Distinct from SPARKWING_NO_BINCACHE
	// (which gates the bincache compiled-pipeline-binary cache).
	noCache bool
	// --dry-run runs each step's DryRunFn instead of its apply Fn.
	// No mutation; safe to run from agents and CI gates before
	// destructive operations. Steps without a DryRunFn (and without
	// an explicit SafeWithoutDryRun marker) soft-skip with reason
	// `no_dry_run_defined` so the contract gap is visible.
	dryRun bool
	// allow is the union of risk labels the operator authorizes via
	// --sw-allow (repeatable; comma-separated allowed). The gate
	// walks the plan's declared labels, subtracts this set, and
	// refuses dispatch if any remain. --sw-dry-run bypasses
	// regardless. The gate degrades gracefully (no labels declared =
	// no block).
	allow []string
	// localOnly forces SQLite state + filesystem logs + filesystem
	// cache for this run, ignoring any configured shared backends.
	// The escape hatch when shared state is misbehaving (stale
	// Postgres, unreachable controller, broken bucket policy) and the
	// operator wants to run against the laptop only.
	localOnly bool
	// index is a git index the caller wants this run's steps to read
	// and write instead of the repository's own, from --sw-index. A
	// verifier uses it to hand a pipeline a staged snapshot of work
	// that is not committed yet, so steps scoped to the staged diff
	// judge that snapshot. Deliberately a flag and not an environment
	// variable: git exports GIT_INDEX_FILE to every hook it launches,
	// sparkwing drops it on startup so the gated repository cannot
	// leak into a pipeline's own work, and an argument is how a caller
	// says the binding is intent rather than inheritance.
	index string
}

// collectPipelineArgs parses passthrough into TriggerRequest.Args.
// Bare flags map to "true". No schema validation here: the controller
// re-parses against the remote pipeline's own schema.
func collectPipelineArgs(passthrough []string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(passthrough) {
		tok := passthrough[i]
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

// appendCSV splits a comma-separated value and appends non-empty
// entries to out. Used by repeatable flags that also accept
// comma-separated lists (pflag StringSlice semantics).
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

// parseRunFlags splits sparkwing-owned (sw-prefixed) flags from
// pass-through args. Unknown / malformed-trailing flags fall through
// to the pipeline binary.
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

// EventIndexBound is the run-stream event `sparkwing run --sw-index`
// writes once the binding is in place, when the run's stream is JSON.
// Its `path` attribute is the absolute index the run's steps will read.
//
// It is a receipt, and callers are meant to require it. Binding an
// index is a request to have a pipeline judge that index; a binary
// with no --sw-index forwards the flag to the pipeline and judges the
// repository's own index instead, and nothing in the exit code tells
// those apart. A caller that sees no index_bound knows the index it
// supplied went unread, and can report that it verified nothing rather
// than reporting a pass.
const EventIndexBound = "index_bound"

// The run stream formats a receipt can be written in. quiet and any
// unrecognized spelling render as prose, since only a caller parsing
// the stream asks for json.
const (
	logFormatJSON   = "json"
	logFormatPretty = "pretty"
)

// bindRunIndex points a run's steps at the git index at path by
// setting GIT_INDEX_FILE in env, and writes the index_bound receipt to
// out in logFormat. The returned env replaces any inherited
// GIT_INDEX_FILE, so the caller's index wins over the ambient one
// rather than shadowing it.
//
// A path that does not exist is refused: git reads a missing index as
// an empty one, so the steps would report a clean tree they were never
// shown.
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

// announceIndexBound writes the receipt in the format the rest of the
// run speaks: the record a caller parses when the stream is json, a
// line when a person is reading the run go by.
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

// setupRefWorktree creates a git worktree at ref. Caller must defer cleanup.
// Best-effort fetch first so unseen refs resolve; fetch failure is non-fatal.
func setupRefWorktree(sparkwingDir, ref string) (worktreeDir, sparkwingSub string, cleanup func(), err error) {
	repoRoot := filepath.Dir(sparkwingDir)

	tmpDir, err := os.MkdirTemp("", "sparkwing-from-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	_ = exec.Command("git", "-C", repoRoot, "fetch", "--quiet", "origin", ref).Run()

	out, err := exec.Command("git", "-C", repoRoot,
		"worktree", "add", "--detach", "--quiet", tmpDir, ref).CombinedOutput()
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
			"worktree", "remove", "--force", tmpDir).Run()
		_ = os.RemoveAll(tmpDir)
	}
	return tmpDir, sub, cleanup, nil
}

// triggerSource builds the trigger_source string a remote dispatch
// records, tagging the originating verb so runs are distinguishable in
// `runs list`: "pipeline-trigger@host" for `pipeline trigger`. Falls
// back to the bare prefix when the hostname can't be read.
func triggerSource(prefix string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return prefix + "@" + host
	}
	return prefix
}

// createRemoteTrigger builds and POSTs a TriggerRequest to prof's
// controller, returning the controller's response. It backs `sparkwing
// pipeline trigger`. It does NOT print or tail -- the caller decides how
// to report. prof must already carry a controller.
func createRemoteTrigger(prof *profile.Profile, pipelineName, source string, wf runFlags, passthrough []string) (*client.TriggerResponse, error) {
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

	if repoURL != "" {
		discoverCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
		services, derr := discovery.ServicesFor(discoverCtx, prof.ControllerURL(), prof.ControllerToken())
		dCancel()
		seedTriggerSource(prof, services.CachePod, derr, repoURL, sha)
	}

	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	resp, err := c.CreateTrigger(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("create trigger on %s: %w", prof.Name, err)
	}
	return resp, nil
}

func seedTriggerSource(prof *profile.Profile, cacheURL string, discoveryErr error, repoURL, sha string) {
	repoDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sparkwing run: gitcache seed skipped (cwd: %v)\n", err)
		return
	}
	if cacheURL != "" {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = bincache.RefreshRepo(refreshCtx, cacheURL, repoURL)
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
	err = bincache.RefreshRepoViaController(refreshCtx, prof.ControllerURL(), prof.ControllerToken(), repoURL)
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

// detectRemoteGit reads cwd's git state. Unresolved fields return empty.
func detectRemoteGit() (branch, sha, repo, repoURL string) {
	return gitContextIn("")
}

// gitContextIn reads dir's git state, or cwd's when dir is empty.
// Unresolved fields return empty: a project with no remote, or no git at
// all, still runs -- it just records less provenance.
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

// parseGithubOwnerRepo extracts "owner/name" from github SSH/HTTPS URLs;
// empty for non-github hosts so warm-runner doesn't attempt unknown clones.
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
