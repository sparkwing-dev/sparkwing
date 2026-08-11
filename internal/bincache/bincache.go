// Package bincache wraps the sparkwing-cache HTTP endpoints that
// distribute compiled .sparkwing/ pipeline binaries and archived
// source trees.
//
// Endpoints:
//
//   - GET /archive?repo=URL&branch=B returns a gzipped tarball of the
//     repo at the branch's HEAD. FetchPipelineSource extracts it and
//     returns the path to the extracted .sparkwing/ dir.
//   - GET /bin/<hash> returns a precompiled binary matching the
//     source hash. TryBinary downloads it to dest.
//   - PUT /bin/<hash> uploads a freshly-compiled binary. UploadBinary
//     does the PUT (authed via a bearer token).
package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// ErrMiss is the sentinel for 404 from /bin/<hash>.
var ErrMiss = errors.New("remote binary cache: miss")

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// CacheURL returns the sparkwing-cache base URL from
// SPARKWING_GITCACHE_URL, stripped of trailing slashes. Empty means
// "no cache available".
func CacheURL() string {
	return strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/")
}

// CacheToken returns the bearer used for PUT /bin/<hash>. Empty
// disables uploads.
func CacheToken() string {
	return os.Getenv("SPARKWING_CACHE_TOKEN")
}

// TryBinary fetches /bin/<hash> from the cache server into dest.
// Returns ErrMiss on 404.
func TryBinary(gcURL, hash, dest string) error {
	req, err := http.NewRequest(http.MethodGet, gcURL+"/bin/"+hash, nil)
	if err != nil {
		return err
	}
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrMiss
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bin cache GET: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// UploadBinary PUTs a compiled binary to /bin/<hash>. Empty token
// sends the request unauthenticated.
func UploadBinary(gcURL, token, hash, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, gcURL+"/bin/"+hash, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cli := &http.Client{Timeout: 60 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bin cache PUT %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// FetchPipelineSource lands the given git repo's source tree at the
// trigger's exact SHA (or the branch tip if no SHA is empty) under
// parentDir/<name> via sparkwing-cache's git smart-HTTP endpoint, and
// returns the path to the cloned tree's .sparkwing subdirectory.
//
// Cluster runners need a real .git so the SDK's git helpers work
// without env-var stamping. depth=1 keeps the on-disk footprint small.
//
// Pinning to a non-empty sha requires
// uploadpack.allowReachableSHA1InWant on the cache pod's bare mirrors.
// The repo is registered idempotently with the cache pod first so a
// cold cache backfills from the canonical SSH URL on the first request.
func FetchPipelineSource(gcURL, repoSSH, branch, sha, parentDir string) (sparkwingDir string, err error) {
	if gcURL == "" {
		return "", fmt.Errorf("FetchPipelineSource: SPARKWING_GITCACHE_URL not set")
	}
	repoSSH, err = sourceurl.ValidateCloneURL(repoSSH)
	if err != nil {
		return "", fmt.Errorf("FetchPipelineSource: invalid repo URL: %w", err)
	}
	if branch == "" {
		branch = "main"
	}
	name := RepoNameFromURL(repoSSH)
	if name == "" {
		return "", fmt.Errorf("FetchPipelineSource: cannot derive repo name from %q", repoSSH)
	}

	if err := registerRepoWithCache(gcURL, name, repoSSH); err != nil {
		return "", fmt.Errorf("git register: %w", err)
	}

	cloneURL := strings.TrimRight(gcURL, "/") + "/git/" + name
	workTree := filepath.Join(parentDir, name)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", err
	}
	if err := os.RemoveAll(workTree); err != nil {
		return "", fmt.Errorf("clear workTree: %w", err)
	}

	if sha != "" {
		if err := fetchExactSHA(cloneURL, sha, workTree); err != nil {
			return "", err
		}
	} else {
		if err := shallowCloneBranch(cloneURL, branch, workTree); err != nil {
			return "", err
		}
	}

	candidate := filepath.Join(workTree, ".sparkwing")
	if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("cloned tree has no .sparkwing directory under %s", workTree)
}

// fetchExactSHA fetches just the requested SHA at depth 1 and checks
// it out. Requires uploadpack.allowReachableSHA1InWant on the server.
func fetchExactSHA(cloneURL, sha, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	runIn := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dest
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.CombinedOutput()
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", cloneURL},
		{"fetch", "--depth", "1", "origin", sha},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, step := range steps {
		if out, err := runIn(step...); err != nil {
			return fmt.Errorf("git %s (sha %s): %w: %s",
				strings.Join(step, " "), sha, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// shallowCloneBranch runs `git clone --depth 1 --single-branch
// --branch B URL DEST` for the no-SHA fallback path.
func shallowCloneBranch(cloneURL, branch, dest string) error {
	cmd := exec.Command(
		"git", "clone",
		"--depth", "1",
		"--single-branch",
		"--branch", branch,
		cloneURL, dest,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s (branch %s): %w: %s",
			cloneURL, branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// registerRepoWithCache POSTs /git/register so the cache pod knows the
// canonical SSH URL for `name`. Idempotent for matching URL; only a
// name conflict errors.
func registerRepoWithCache(gcURL, name, repoURL string) error {
	q := neturl.Values{}
	q.Set("name", name)
	q.Set("repo", repoURL)
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(gcURL, "/")+"/git/register?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	cli := &http.Client{Timeout: 30 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RefreshRepo POSTs /git/refresh on the cache so a freshly-pushed SHA
// is mirrored before the runner tries to fetch it. Best-effort: the
// caller supplies a short timeout and logs / continues on failure
// (the trigger-loop fetch retry will catch the residual race). Returns
// nil if the cache acks 2xx, an error otherwise. Empty repoURL is a
// programmer error and returns immediately.
//
// The dispatcher (cmd/sparkwing/run_dispatch.go dispatchRemote) calls
// this before CreateTrigger to close the
//
//	git push origin main
//	sparkwing run X --on prod   # immediately
//
// race that surfaces as "fatal: remote error: upload-pack: not our
// ref <sha>" when the cache's 30s background-fetch loop hasn't
// caught up yet.
func RefreshRepo(ctx context.Context, gcURL, repoURL string) error {
	if gcURL == "" {
		return fmt.Errorf("RefreshRepo: gitcache URL required")
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return fmt.Errorf("RefreshRepo: invalid repo URL: %w", err)
	}
	q := neturl.Values{}
	q.Set("repo", repoURL)
	url := strings.TrimRight(gcURL, "/") + "/git/refresh?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// SeedRepo creates a git bundle from repoDir and uploads it to
// sparkwing-cache. It is the fallback when the cache cannot clone the
// origin itself.
func SeedRepo(ctx context.Context, gcURL, token, repoURL, repoDir, sha string) error {
	if gcURL == "" {
		return fmt.Errorf("SeedRepo: gitcache URL required")
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return fmt.Errorf("SeedRepo: invalid repo URL: %w", err)
	}
	sha, err = validateGitObject(sha)
	if err != nil {
		return err
	}
	bundle, err := createRepoBundle(ctx, repoDir, sha)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(bundle) }()

	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	q := neturl.Values{}
	q.Set("repo", repoURL)
	q.Set("sha", sha)
	url := strings.TrimRight(gcURL, "/") + "/sync/seed?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, f)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func createRepoBundle(ctx context.Context, repoDir, sha string) (string, error) {
	sha, err := validateGitObject(sha)
	if err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--verify", sha+"^{commit}").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git verify seed commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	tmp, err := os.CreateTemp("", "sparkwing-repo-*.bundle")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	_ = os.Remove(path)

	ref := "refs/sparkwing-seed/" + sha
	if out, err := exec.CommandContext(ctx, "git", "-C", repoDir, "update-ref", ref, sha).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git seed ref: %w: %s", err, strings.TrimSpace(string(out)))
	}
	defer func() {
		_ = exec.CommandContext(context.Background(), "git", "-C", repoDir, "update-ref", "-d", ref).Run()
	}()

	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "bundle", "create", path, ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("git bundle create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

func validateGitObject(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !gitObjectRE.MatchString(sha) {
		return "", fmt.Errorf("git sha must be a 40-64 character hex object id")
	}
	return sha, nil
}

// RefreshRepoViaController asks a controller to proxy a refresh to its
// configured cache.
func RefreshRepoViaController(ctx context.Context, controllerURL, token, repoURL string) error {
	if controllerURL == "" {
		return fmt.Errorf("RefreshRepoViaController: controller URL required")
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return fmt.Errorf("RefreshRepoViaController: invalid repo URL: %w", err)
	}
	q := neturl.Values{}
	q.Set("repo", repoURL)
	url := strings.TrimRight(controllerURL, "/") + "/api/v1/gitcache/refresh?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// SeedRepoViaController uploads a git bundle through the controller to
// its configured cache.
func SeedRepoViaController(ctx context.Context, controllerURL, token, repoURL, repoDir, sha string) error {
	if controllerURL == "" {
		return fmt.Errorf("SeedRepoViaController: controller URL required")
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return fmt.Errorf("SeedRepoViaController: invalid repo URL: %w", err)
	}
	sha, err = validateGitObject(sha)
	if err != nil {
		return err
	}
	bundle, err := createRepoBundle(ctx, repoDir, sha)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(bundle) }()

	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	q := neturl.Values{}
	q.Set("repo", repoURL)
	q.Set("sha", sha)
	url := strings.TrimRight(controllerURL, "/") + "/api/v1/gitcache/seed?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, f)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RepoNameFromURL returns the friendly name registered with the cache
// pod for a given repo URL. Strips trailing .git and returns the path
// component after the final "/" or ":". Empty for malformed input.
func RepoNameFromURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	repoURL = strings.TrimSuffix(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	if i := strings.LastIndexAny(repoURL, "/:"); i >= 0 {
		return repoURL[i+1:]
	}
	return repoURL
}

// RepoURLFromGitHub converts a "owner/repo" full_name into an SSH URL.
// SSH so the cache can reach private repos via its deploy key.
func RepoURLFromGitHub(fullName string) string {
	if fullName == "" {
		return ""
	}
	if strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return fullName
	}
	return "git@github.com:" + fullName + ".git"
}

// SparkwingHome honors SPARKWING_HOME if set, otherwise ~/.sparkwing.
func SparkwingHome() string {
	if h := os.Getenv("SPARKWING_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sparkwing")
}

// ErrMissingGoSum is returned by CompilePipeline when `go build`
// fails because go.sum doesn't list every module that go.mod requires.
// Recoverable by `go mod download`.
var ErrMissingGoSum = errors.New("missing go.sum entries")

// CompileError wraps a `go build` failure with the combined stdout +
// stderr of the build. Callers that want to surface the real toolchain
// output (e.g. the warm-runner's trigger loop, which streams it into
// the run's logs) extract the bytes via errors.As; callers that only
// want the terse wrapper string (`compile .sparkwing/: <exit>`) keep
// working unchanged.
type CompileError struct {
	Output []byte // combined stdout + stderr captured during `go build`
	Err    error  // underlying error (typically *exec.ExitError)
}

func (e *CompileError) Error() string { return fmt.Sprintf("compile .sparkwing/: %v", e.Err) }
func (e *CompileError) Unwrap() error { return e.Err }

// lockedBuffer is a mutex-guarded bytes.Buffer. exec.Cmd drains
// stdout and stderr from separate goroutines; when both target the
// same buffer (as CompilePipeline does to interleave them in capture
// order), the writes need serialization or the race detector trips.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

func (lb *lockedBuffer) Bytes() []byte {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return append([]byte(nil), lb.buf.Bytes()...)
}

// CompilePipeline `go build`s sparkwingDir -> dest. Stdout + stderr
// stream to the parent's stderr (so pod logs still show progress);
// both are also captured into a buffer so a failure returns a
// *CompileError with the toolchain output. Missing-go.sum is
// detected up front and surfaced as ErrMissingGoSum so callers can
// retry after `go mod download`.
//
// If `.sparkwing/.resolved.mod` exists, compile is invoked with
// `-modfile=<path>` so the overlay's resolved versions take precedence
// over the git-tracked go.mod. When a `go.work` is in scope, the
// overlay is skipped (the toolchain refuses `-modfile` in workspace
// mode); the workspace's module resolution wins, and a single-line
// warning is written to stderr so the operator knows sparks pinning
// is dormant for this build.
func CompilePipeline(sparkwingDir, dest string) error {
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via `go build`.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run",
		)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// -trimpath keeps the build directory out of the output. Without
	// it two checkouts of one commit compile to different bytes, so a
	// cache key shared between them would be a lie: the second checkout
	// would run a binary carrying the first one's absolute paths. The
	// cost is that panics and runtime.Caller report module-relative
	// paths rather than paths on this machine.
	args := []string{"build", "-trimpath"}
	env := os.Environ()
	overlay := overlayModfilePath(sparkwingDir)
	work, workPresent := goWorkInScope(sparkwingDir)
	switch {
	case workPresent && !goWorkCovers(work, sparkwingDir):
		fmt.Fprintf(os.Stderr,
			"warning: %s does not include this .sparkwing module; ignoring it. "+
				"A sparkwing project builds as a self-contained module, not nested "+
				"inside another Go workspace; add it to the go.work `use` list to "+
				"link it deliberately.\n",
			work,
		)
		env = withGoworkOff(env)
		if overlay != "" {
			args = append(args, "-modfile="+overlay)
		}
	case workPresent:
		if overlay != "" {
			fmt.Fprintf(os.Stderr,
				"warning: %s in effect; skipping sparks resolution. "+
					"Modules resolve from go.mod + workspace, not .resolved.mod. "+
					"To use local copies of sparks libs too, add them to go.work.\n",
				work,
			)
		}
	case overlay != "":
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, "-o", dest, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = sparkwingDir
	var captured lockedBuffer
	cmd.Stdout = io.MultiWriter(os.Stderr, &captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		if strings.Contains(captured.String(), "missing go.sum entry") {
			return ErrMissingGoSum
		}
		return &CompileError{Output: captured.Bytes(), Err: err}
	}
	return nil
}

// overlayModfilePath returns the path to `.sparkwing/.resolved.mod`
// if present as a regular file, else "".
func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

// goWorkInScope walks up from sparkwingDir looking for a `go.work`
// file, the same way `go build` discovers workspace mode. Returns the
// path + true on hit, "" + false otherwise. Honors GOWORK if set
// ("off" disables; an explicit path is used as-is when readable).
func goWorkInScope(sparkwingDir string) (string, bool) {
	switch env := os.Getenv("GOWORK"); env {
	case "off":
		return "", false
	case "":
	default:
		if fi, err := os.Stat(env); err == nil && fi.Mode().IsRegular() {
			return env, true
		}
		return "", false
	}
	dir := sparkwingDir
	for {
		candidate := filepath.Join(dir, "go.work")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// goWorkCovers reports whether the workspace at workPath lists moduleDir
// in its `use` directives. When it does not, `go build .` inside moduleDir
// would resolve against the workspace and fail because the workspace's
// main modules do not contain the package -- so the caller ignores such a
// workspace and builds the module standalone. A parse failure is treated
// as not-covering, the conservative choice.
func goWorkCovers(workPath, moduleDir string) bool {
	raw, err := os.ReadFile(workPath)
	if err != nil {
		return false
	}
	wf, err := modfile.ParseWork(workPath, raw, nil)
	if err != nil {
		return false
	}
	absModule, err := filepath.Abs(moduleDir)
	if err != nil {
		return false
	}
	workDir := filepath.Dir(workPath)
	for _, u := range wf.Use {
		p := u.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(workDir, p)
		}
		ap, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if filepath.Clean(ap) == filepath.Clean(absModule) {
			return true
		}
	}
	return false
}

// withGoworkOff returns env with any GOWORK entry replaced by GOWORK=off,
// so a build ignores an enclosing workspace that does not cover the
// module being built.
func withGoworkOff(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "GOWORK=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}

// PipelineCacheKey returns a 16-char hex fingerprint of the pipeline
// module contents plus every local replace target. Hashes for the
// host's platform; cross-compile callers use
// PipelineCacheKeyForPlatform.
//
// Format: aaaaaaaa-bbbbbbbb (8-8 split).
func PipelineCacheKey(sparkwingDir string) (string, error) {
	return PipelineCacheKeyForPlatform(sparkwingDir, runtime.GOOS, runtime.GOARCH)
}

// PipelineCacheKeyForPlatform is PipelineCacheKey with explicit
// GOOS/GOARCH inputs (runtime.GOOS/GOARCH are baked at host-build time
// and don't reflect post-Setenv changes).
//
// Local replace targets are folded in by content, not by version: a
// module replaced to a filesystem path (via the pipeline's go.mod or an
// in-scope go.work) carries no version the key could pin, so the whole
// directory is hashed. All files are hashed, not just Go source, so
// editing an embedded asset (a replaced template registry's manifests)
// invalidates the compiled binary. With no local replace targets and no
// covering workspace the walk is skipped entirely and the key stays a
// hash of the module tree, go.mod, and the overlays.
func PipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	parts, err := keyParts(sparkwingDir, goos, goarch)
	if err != nil {
		return "", err
	}
	return foldKey(parts), nil
}

// KeyPart is one labeled input to the cache key. Splitting the key into
// named parts is what lets `sparkwing cache explain` say which input
// changed, instead of reporting that an opaque hash moved.
type KeyPart struct {
	Label  string // what this input is, e.g. "module tree" or a module path
	Digest string // sha256 of this part alone, for comparing across builds
	Detail string // human note: file counts, sizes, what was excluded
	// material is the exact bytes folded into the key. Parts carry it so
	// the explanation and the key are computed from one source and
	// cannot drift apart.
	material []byte
}

// foldKey concatenates the parts' material in order and takes the
// leading 16 hex digits, split for readability.
func foldKey(parts []KeyPart) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p.material)
	}
	raw := fmt.Sprintf("%x", h.Sum(nil))
	return raw[:8] + "-" + raw[8:16]
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])[:12]
}

// ExplainCacheKey returns the key together with the inputs that produced
// it, so an operator can see why a rebuild happened without reading the
// source of this package.
func ExplainCacheKey(sparkwingDir string) (string, []KeyPart, error) {
	parts, err := keyParts(sparkwingDir, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", nil, err
	}
	return foldKey(parts), parts, nil
}

// keyParts builds the ordered inputs to the cache key. The concatenated
// material is the hashed preimage, so changing what is appended here
// changes the key.
func keyParts(sparkwingDir, goos, goarch string) ([]KeyPart, error) {
	var parts []KeyPart
	add := func(label, detail string, material []byte) {
		parts = append(parts, KeyPart{
			Label: label, Detail: detail,
			Digest: digestOf(material), material: material,
		})
	}

	add("go toolchain", goMajorMinor(), []byte(fmt.Sprintf("go:%s\n", goMajorMinor())))
	add("platform", goos+"/"+goarch, []byte(fmt.Sprintf("arch:%s/%s\n", goos, goarch)))

	var moduleBuf bytes.Buffer
	moduleStats, err := hashDirIntoCounted(&moduleBuf, sparkwingDir, allFiles)
	if err != nil {
		return nil, err
	}
	add("module tree", moduleStats.String(), moduleBuf.Bytes())

	goModPath := filepath.Join(sparkwingDir, "go.mod")
	replaceTargets, err := localReplaceTargets(goModPath)
	if err != nil {
		return nil, err
	}
	workTargets, workSummary, err := localWorkspaceTargets(sparkwingDir)
	if err != nil {
		return nil, err
	}
	if workSummary != "" {
		add("go.work", "normalized directives", []byte(workSummary))
	}

	replaceTargets = append(replaceTargets, workTargets...)
	sort.Slice(replaceTargets, func(i, j int) bool {
		if replaceTargets[i].Label != replaceTargets[j].Label {
			return replaceTargets[i].Label < replaceTargets[j].Label
		}
		return replaceTargets[i].Dir < replaceTargets[j].Dir
	})
	var last replaceTarget
	for _, t := range replaceTargets {
		if t == last {
			continue
		}
		last = t
		var buf bytes.Buffer
		// Only the label reaches the digest. The directory is read for
		// content below but never recorded, so the same module at a
		// different path still yields the same key.
		fmt.Fprintf(&buf, "replace:%s\n", t.Label)
		stats, err := hashDirIntoCounted(&buf, t.Dir, allFiles)
		if err != nil {
			return nil, err
		}
		add("replace "+t.Label, stats.String()+" from "+t.Dir, buf.Bytes())
	}

	for _, overlay := range []struct {
		name   string
		prefix string
	}{
		{".resolved.mod", "resolved-mod:"},
		{".resolved.sum", "resolved-sum:"},
	} {
		p := filepath.Join(sparkwingDir, overlay.name)
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var buf bytes.Buffer
		fmt.Fprint(&buf, overlay.prefix)
		buf.Write(data)
		fmt.Fprintln(&buf)
		add(overlay.name, "resolved module pins", buf.Bytes())
	}

	return parts, nil
}

// ExecReplace replaces the current process image with the target
// binary via syscall.Exec. Windows has no exec(2)-equivalent; falls
// back to fork+exec and propagates the child's exit code.
func ExecReplace(bin string, args []string, dir string, env []string) error {
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		return execChildWindows(bin, args, env)
	}
	argv := append([]string{bin}, args...)
	return syscall.Exec(bin, argv, env)
}

// execChildWindows runs bin as a foreground subprocess and exits with
// the child's status code. Returns only on spawn failure.
//
// The two os.Exit calls below are deliberate: this function is the
// Windows half of ExecReplace, whose POSIX path uses syscall.Exec to
// replace the current process. ExecReplace's contract is "this
// process disappears, replaced by the child's exit status"; returning
// here would violate that contract.
func execChildWindows(bin string, args, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode()) //nolint:forbidigo // mirrors syscall.Exec exit-with-child semantics on POSIX
		}
		return err
	}
	os.Exit(0) //nolint:forbidigo // mirrors syscall.Exec exit-with-child semantics on POSIX
	return nil
}

type fileFilter func(name string) bool

func allFiles(string) bool { return true }

// goMajorMinor returns runtime.Version()'s "go1.26" prefix, stripping
// the patch component.
func goMajorMinor() string {
	v := runtime.Version()
	dots := 0
	for i, c := range v {
		if c == '.' {
			dots++
			if dots == 2 {
				return v[:i]
			}
		}
	}
	return v
}

// hashDirInto folds every build-relevant file under dir into h as
// "<path relative to dir>\x00<sha256 of contents>". Paths are relative
// and digests are content-only -- no mtime, size, or inode -- so two
// checkouts holding identical files produce identical bytes here
// regardless of where they sit on disk.
//
// Files git ignores are skipped; see [ignoredUnder] for why that is
// what makes the result portable rather than merely cheaper.
func hashDirInto(h io.Writer, dir string, keep fileFilter) error {
	_, err := hashDirIntoCounted(h, dir, keep)
	return err
}

// HashStats describes what a directory contributed to the key. It is
// reported by `sparkwing cache explain` so the exclusion of gitignored
// files is visible rather than a silent surprise when an edit fails to
// trigger a rebuild.
type HashStats struct {
	Files   int   // files hashed
	Bytes   int64 // their total size
	Ignored int   // files skipped because git ignores them
}

func (s HashStats) String() string {
	base := fmt.Sprintf("%d files, %s", s.Files, humanSize(s.Bytes))
	if s.Ignored > 0 {
		base += fmt.Sprintf(" (%d gitignored, excluded)", s.Ignored)
	}
	return base
}

// humanSize renders a byte count compactly for explain output.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for cur := n / unit; cur >= unit && exp < 3; cur /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// hashDirIntoCounted is hashDirInto with a report of what it covered.
func hashDirIntoCounted(h io.Writer, dir string, keep fileFilter) (HashStats, error) {
	var stats HashStats
	files, err := walkHashable(dir, keep)
	if err != nil {
		return stats, err
	}
	ignored := ignoredUnder(dir, files)
	for _, path := range files {
		if ignored[path] {
			stats.Ignored++
			continue
		}
		rel, _ := filepath.Rel(dir, path)
		f, err := os.Open(path)
		if err != nil {
			return stats, err
		}
		fileH := sha256.New()
		n, copyErr := io.Copy(fileH, f)
		f.Close()
		if copyErr != nil {
			return stats, copyErr
		}
		stats.Files++
		stats.Bytes += n
		fmt.Fprintf(h, "%s\x00%x\n", rel, fileH.Sum(nil))
	}
	return stats, nil
}

// walkHashable lists the files under dir that keep admits, in the
// lexical order [filepath.WalkDir] guarantees, so the digest a caller
// builds from them is stable. Reading contents is deferred to the
// caller because the ignore check is one batched call over the whole
// list, and skipping a file is far cheaper than opening it.
func walkHashable(dir string, keep fileFilter) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude-scratch", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !keep(d.Name()) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// replaceTarget is one local module folded into the cache key. Label is
// the module identity recorded in the digest; Dir is where that module
// happens to live on this machine and is read for content but never
// written into the key. Keeping the two apart is what lets two
// checkouts of one commit agree on a key from different paths.
type replaceTarget struct {
	Label string
	Dir   string
}

// replaceLabel renders a replaced module's identity. The version is
// part of it because `replace foo v1.2.3 => ./foo` and a blanket
// `replace foo => ./foo` are different directives and must not collide.
func replaceLabel(old module.Version) string {
	if old.Version == "" {
		return old.Path
	}
	return old.Path + "@" + old.Version
}

// moduleLabelOf reads the module path declared by dir's own go.mod. A
// workspace `use` directive names a directory rather than a module, so
// this is how such a target acquires a portable identity. Use.ModulePath
// is deliberately not consulted: it is documented as the path found in a
// comment and is not reliably populated.
func moduleLabelOf(dir string) (string, error) {
	p := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("workspace module %s: %w", dir, err)
	}
	path := modfile.ModulePath(data)
	if path == "" {
		return "", fmt.Errorf("workspace module %s: no module directive in go.mod", dir)
	}
	return path, nil
}

// localReplaceTargets returns every local-path replace directive in
// go.mod, labeled by the module it replaces. Remote replaces are
// ignored (the go.mod hash already covers them).
func localReplaceTargets(goModPath string) ([]replaceTarget, error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, err
	}
	mf, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(goModPath)
	var out []replaceTarget
	for _, r := range mf.Replace {
		np := r.New.Path
		if np == "" || !isLocalPath(np) {
			continue
		}
		abs := np
		if !filepath.IsAbs(abs) {
			abs = filepath.Clean(filepath.Join(dir, np))
		}
		out = append(out, replaceTarget{Label: replaceLabel(r.Old), Dir: abs})
	}
	return out, nil
}

func isLocalPath(p string) bool {
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/")
}

// localWorkspaceTargets returns the local modules an in-scope go.work
// contributes to the pipeline build -- its `use` modules and any
// filesystem-path `replace` targets -- along with a normalized summary
// of the workspace's own build-affecting directives. It mirrors
// CompilePipeline's workspace decision: a workspace that does not cover
// sparkwingDir is ignored (the build disables it via GOWORK=off), and
// sparkwingDir itself is excluded because the caller already hashes it.
// When no workspace applies, both results are empty and the caller's
// no-replace fast path is untouched.
//
// The summary is normalized rather than a hash of the file's bytes so
// that comments, directive order, and the spelling of a `use` path --
// all of which differ between two checkouts of one commit -- do not
// perturb the key. It enumerates every field [modfile.WorkFile] carries
// (Go, Toolchain, Godebug, Use, Replace); hashing raw bytes covered
// those by accident, so anything omitted here silently stops
// invalidating the cache.
func localWorkspaceTargets(sparkwingDir string) (targets []replaceTarget, summary string, err error) {
	work, ok := goWorkInScope(sparkwingDir)
	if !ok || !goWorkCovers(work, sparkwingDir) {
		return nil, "", nil
	}
	raw, err := os.ReadFile(work)
	if err != nil {
		return nil, "", err
	}
	wf, err := modfile.ParseWork(work, raw, nil)
	if err != nil {
		return nil, "", err
	}
	absSparkwing, err := filepath.Abs(sparkwingDir)
	if err != nil {
		return nil, "", err
	}
	absSparkwing = filepath.Clean(absSparkwing)
	workDir := filepath.Dir(work)

	resolve := func(p string) string {
		if p == "" {
			return ""
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workDir, p)
		}
		abs = filepath.Clean(abs)
		if abs == absSparkwing {
			return ""
		}
		return abs
	}

	var b strings.Builder
	b.WriteString("go.work\n")
	if wf.Go != nil {
		fmt.Fprintf(&b, "go:%s\n", wf.Go.Version)
	}
	if wf.Toolchain != nil {
		fmt.Fprintf(&b, "toolchain:%s\n", wf.Toolchain.Name)
	}
	godebugs := make([]string, 0, len(wf.Godebug))
	for _, g := range wf.Godebug {
		godebugs = append(godebugs, g.Key+"="+g.Value)
	}
	sort.Strings(godebugs)
	for _, g := range godebugs {
		fmt.Fprintf(&b, "godebug:%s\n", g)
	}

	uses := make([]string, 0, len(wf.Use))
	for _, u := range wf.Use {
		abs := resolve(u.Path)
		if abs == "" {
			continue
		}
		label, err := moduleLabelOf(abs)
		if err != nil {
			return nil, "", err
		}
		targets = append(targets, replaceTarget{Label: label, Dir: abs})
		uses = append(uses, label)
	}
	sort.Strings(uses)
	for _, u := range uses {
		fmt.Fprintf(&b, "use:%s\n", u)
	}

	replaces := make([]string, 0, len(wf.Replace))
	for _, r := range wf.Replace {
		label := replaceLabel(r.Old)
		if isLocalPath(r.New.Path) {
			abs := resolve(r.New.Path)
			if abs == "" {
				continue
			}
			targets = append(targets, replaceTarget{Label: label, Dir: abs})
			// The replacement's location is deliberately omitted; its
			// contents are hashed through the target instead.
			replaces = append(replaces, label+" => local")
			continue
		}
		replaces = append(replaces, label+" => "+replaceLabel(r.New))
	}
	sort.Strings(replaces)
	for _, r := range replaces {
		fmt.Fprintf(&b, "replace:%s\n", r)
	}

	return targets, b.String(), nil
}
