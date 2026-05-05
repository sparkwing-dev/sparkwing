// Package bincache wraps the sparkwing-cache HTTP endpoints that
// distribute compiled .sparkwing/ pipeline binaries and archived
// source trees. Used by both the `wing` compile path (to pull a
// precompiled binary before falling back to `go build`) and the
// sparkwing-fleet-worker (to fetch foreign repo sources on demand).
//
// Split out of cmd/sparkwing's internal helpers during the binary
// split (2026-04-22) so cmd/sparkwing-fleet-worker can use the same
// primitives without the packages depending on each other.
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
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/mod/modfile"
)

// ErrMiss is the sentinel for 404 from /bin/<hash>. Callers check it
// to distinguish "binary not built yet" from "cache server
// unreachable."
var ErrMiss = errors.New("remote binary cache: miss")

// CacheURL returns the sparkwing-cache base URL from the
// SPARKWING_GITCACHE_URL env var, stripped of trailing slashes. Empty
// means "no cache available" -- callers that fall back to local
// compilation short-circuit on this.
func CacheURL() string {
	return strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/")
}

// CacheToken returns the bearer used for PUT /bin/<hash>. GETs are
// unauthenticated inside the cluster. Empty is fine when uploads
// aren't configured -- the upload step silently skips.
func CacheToken() string {
	return os.Getenv("SPARKWING_CACHE_TOKEN")
}

// TryBinary fetches /bin/<hash> from the cache server into dest.
// Returns nil on success (binary written + chmod'd 0755), ErrMiss on
// 404, or a wrapped error on other failures.
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
	// Write to a temp file then rename so partial downloads never
	// leave a half-written binary at the canonical path.
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// UploadBinary PUTs a compiled binary to /bin/<hash>. Token is
// required in prod (cache server enforces it on writes); empty token
// sends the request unauthenticated and will 401 against an
// auth-enabled cache.
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
// trigger's exact SHA (or the branch tip if no SHA is supplied) under
// parentDir/<name> via sparkwing-cache's git smart-HTTP endpoint, and
// returns the path to the cloned tree's .sparkwing subdirectory.
// Callers `go build` that directory for a pipeline binary.
//
// Cluster runners need a real .git so the SDK's git helpers
// (sparks/docker.ComputeTags, sparkwing/git.CurrentSHA, etc.) work
// without env-var stamping (ISS-031). depth=1 keeps the on-disk
// footprint small; no history beyond the requested commit.
//
// SHA vs branch tip: when sha is non-empty we `git fetch --depth 1
// origin <sha> && git checkout FETCH_HEAD` so the build lands at
// exactly the SHA the webhook recorded, not at whatever the branch
// tip has advanced to between webhook delivery and runner claim.
// Requires uploadpack.allowReachableSHA1InWant on the cache pod's
// bare mirrors (set in handleGitRegister). Empty sha (legitimate
// for CLI dispatches without webhook context) falls back to a
// branch-tip clone.
//
// The clone URL is <gcURL>/git/<name> where <name> is derived from
// the repo URL's basename. The repo is registered idempotently with
// the cache pod first so a cold cache backfills from the canonical
// SSH URL on the first request. Failures bubble up loudly -- the
// prior tarball fallback hid silent-degrade bugs that ate hours of
// debugging time, so this path is intentionally unforgiving.
func FetchPipelineSource(gcURL, repoSSH, branch, sha, parentDir string) (sparkwingDir string, err error) {
	if gcURL == "" {
		return "", fmt.Errorf("FetchPipelineSource: SPARKWING_GITCACHE_URL not set")
	}
	if repoSSH == "" {
		return "", fmt.Errorf("FetchPipelineSource: repo URL required")
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
	// If a previous claim left the workTree behind (RemoveAll is best-
	// effort), git refuses to write into a non-empty dir. Wipe before
	// cloning rather than carrying stale tree state forward.
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

// fetchExactSHA initializes a fresh repo at dest, points origin at
// cloneURL, and fetches just the requested SHA at depth 1 before
// checking it out. Defeats the branch-tip race where HEAD advances
// between trigger persistence and runner claim. Requires the server
// to allow reachable-SHA fetches (uploadpack.allowReachableSHA1InWant
// on the bare mirror).
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
// --branch B URL DEST`. Used for the no-SHA fallback (manual CLI
// dispatch with no webhook payload). Caller is responsible for
// logging that the fallback path was taken so operators can see when
// trigger-time SHA pinning is bypassed.
func shallowCloneBranch(cloneURL, branch, dest string) error {
	cmd := exec.Command("git", "clone",
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

// registerRepoWithCache POSTs /git/register on the cache pod so the
// pod knows the canonical SSH URL for `name` and can backfill from it
// if its mirror is cold. Idempotent: re-registering an existing name
// with the same URL returns 200; only a name conflict (different URL)
// errors. Mirrors the same call `sparkwing push` makes.
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

// RepoNameFromURL returns the friendly name registered with the cache
// pod for a given repo URL. Strips trailing .git and returns the path
// component after the final "/" or ":". Empty for malformed input.
//
// Examples:
//   - git@github.com:sparkwing-dev/sparkwing.git → sparkwing
//   - https://github.com/sparkwing-dev/sparkwing-platform → sparkwing-platform
//   - sparkwing-dev/sparkwing                    → sparkwing
//   - sparkwing                                  → sparkwing
func RepoNameFromURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	repoURL = strings.TrimSuffix(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	if i := strings.LastIndexAny(repoURL, "/:"); i >= 0 {
		return repoURL[i+1:]
	}
	return repoURL
}

// RepoURLFromGitHub converts a `full_name` like "owner/repo"
// (shape GitHub webhook payloads use) into an SSH URL the gitcache
// understands. Prefer SSH so the cache can reach private repos via
// its configured deploy key.
func RepoURLFromGitHub(fullName string) string {
	if fullName == "" {
		return ""
	}
	if strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return fullName
	}
	return "git@github.com:" + fullName + ".git"
}

// --- compile helpers ---
//
// Moved here from cmd/sparkwing/compile.go during the binary split
// so both `wing`'s compile-and-exec path and sparkwing-fleet-worker's
// foreign-repo compile path share one implementation.

// SparkwingHome mirrors the paths logic in pkg/orchestrator: honors
// SPARKWING_HOME if set, otherwise ~/.sparkwing. Duplicated from the
// orchestrator package to avoid cmd/ binaries pulling the whole
// orchestrator into a small compile helper.
func SparkwingHome() string {
	if h := os.Getenv("SPARKWING_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sparkwing")
}

// CachedBinaryPath returns where a pipeline binary with the given
// hash lives. One subdir per hash lets old entries be pruned with a
// single rmdir.
func CachedBinaryPath(hash string) string {
	root := filepath.Join(SparkwingHome(), "cache", "pipelines", hash)
	name := "pipelines"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, name)
}

// ErrMissingGoSum is returned by CompilePipeline when `go build`
// fails because go.sum doesn't list every module that go.mod requires.
// Callers (see cmd/sparkwing/compile.go) recover by running
// `go mod download` and retrying once -- this is the classic "first
// run after `pipeline new` if post-scaffold tidy was skipped" case.
var ErrMissingGoSum = errors.New("missing go.sum entries")

// CompilePipeline `go build`s sparkwingDir -> dest. Stdout + stderr
// go to the parent's stderr so `wing` callers (which may want clean
// stdout) don't see noise on success. Stderr is also captured into a
// small ring buffer so we can detect the "missing go.sum entry" case
// and surface ErrMissingGoSum for caller-side recovery; the live
// display to the user is unchanged.
//
// If the consumer repo ships a sparks overlay modfile
// (`.sparkwing/.resolved.mod`, written by internal/sparks), the
// compile is invoked with `-modfile=<path>` so the overlay's resolved
// versions take precedence over the git-tracked go.mod. Absent overlay
// -> plain `go build` as before. Callers that have already resolved
// sparks externally can just drop a `.resolved.mod` next to go.mod;
// that's the contract REG-011d relies on.
func CompilePipeline(sparkwingDir, dest string) error {
	// Pre-flight: bare `exec.Command("go", ...)` with no Go on PATH
	// fails as `exec: "go": executable file not found in $PATH` --
	// surfaces in user output verbatim and reads as a sparkwing bug.
	// Catch it ourselves and point at the right fix.
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf(
			"go toolchain not on PATH: sparkwing compiles .sparkwing/ via `go build`.\n" +
				"  Install Go 1.26+ from https://go.dev/dl/ and re-run.")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"build"}
	if overlay := overlayModfilePath(sparkwingDir); overlay != "" {
		args = append(args, "-modfile="+overlay)
	}
	args = append(args, "-o", dest, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = sparkwingDir
	cmd.Stdout = os.Stderr
	// Tee stderr: live-display to os.Stderr (so the user sees compile
	// errors as they happen) while also capturing into a buffer so we
	// can post-classify the failure. io.MultiWriter is exactly this.
	var captured bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		// "missing go.sum entry for module providing package X" is
		// `go build`'s diagnostic when go.sum doesn't cover everything
		// in go.mod. Recoverable via `go mod download`. Substring
		// match is stable across Go versions back to ~1.16.
		if strings.Contains(captured.String(), "missing go.sum entry") {
			return ErrMissingGoSum
		}
		return fmt.Errorf("compile .sparkwing/: %w", err)
	}
	return nil
}

// overlayModfilePath returns the absolute path to
// `.sparkwing/.resolved.mod` if present, else "". Checks only a regular
// file; symlinks and dirs are ignored (malformed state, bail out
// cleanly to the no-overlay path).
func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

// PipelineCacheKey returns a 16-char hex fingerprint of the pipeline
// module contents plus every local replace target. Any change to an
// input file, go.mod, or an SDK referenced via `replace` busts the
// cache. Hashes for the host's platform; cross-compile callers
// (LOCAL-006 publish) use PipelineCacheKeyForPlatform.
//
// Format: aaaaaaaa-bbbbbbbb (8-8 split). Matches the cache server's
// /bin/<hash> regex ^[0-9a-f]{8}(-[0-9a-f]{8}){0,3}$.
func PipelineCacheKey(sparkwingDir string) (string, error) {
	return PipelineCacheKeyForPlatform(sparkwingDir, runtime.GOOS, runtime.GOARCH)
}

// PipelineCacheKeyForPlatform is PipelineCacheKey with explicit
// GOOS/GOARCH inputs. Used by `sparkwing pipeline publish` when
// cross-compiling: runtime.GOOS / runtime.GOARCH are baked at host-
// build time, so they don't reflect the target arch even after
// os.Setenv("GOOS", ...). Pass the target platform here so each
// produced binary lands at a distinct cache key.
func PipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	h := sha256.New()

	// Mix in only the major.minor of the Go runtime, not the full
	// patch. Cross-machine binary caching (LOCAL-006) routinely sees
	// `go1.26.0` on a laptop publish and `go1.26.2` on a CI fetch
	// for the same source. Patch releases don't change generated-
	// code ABI in any meaningful way; major.minor jumps do (new
	// opcodes, runtime layout) so we still bust the cache then.
	fmt.Fprintf(h, "go:%s\n", goMajorMinor())
	fmt.Fprintf(h, "arch:%s/%s\n", goos, goarch)

	if err := hashDirInto(h, sparkwingDir, allFiles); err != nil {
		return "", err
	}

	goModPath := filepath.Join(sparkwingDir, "go.mod")
	replaceTargets, err := localReplaceTargets(goModPath)
	if err != nil {
		return "", err
	}
	sort.Strings(replaceTargets)
	for _, t := range replaceTargets {
		fmt.Fprintf(h, "replace:%s\n", t)
		if err := hashDirInto(h, t, goSourceOnly); err != nil {
			return "", err
		}
	}

	// Overlay modfile: once sparks libraries are consumed via
	// .sparkwing/.resolved.mod (REG-011b) instead of local replaces,
	// the replace-target walk above no longer captures sparks version
	// changes. Hash the overlay's contents explicitly so a version
	// bump in .resolved.mod (or its .resolved.sum) busts the cache.
	// Absent files are fine; behavior matches today's when neither
	// exists.
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
			return "", err
		}
		fmt.Fprint(h, overlay.prefix)
		h.Write(data)
		fmt.Fprintln(h)
	}

	raw := fmt.Sprintf("%x", h.Sum(nil))
	return raw[:8] + "-" + raw[8:16], nil
}

// ExecReplace replaces the current process image with the target
// binary via syscall.Exec. Unlike fork-then-exec, this leaves no
// parent wrapper hanging around -- PID 1 sees the compiled pipeline
// binary directly. Critical for long-running invocations like
// `wing worker` where a signal handler targeting the wrapper would
// orphan the actual worker.
//
// Windows has no exec(2)-equivalent; syscall.Exec there returns
// "not supported by windows". Fall back to fork+exec: spawn the
// child, wire stdio, propagate its exit code via os.Exit so the
// parent's exit code matches what the child returned. The wrapper
// sparkwing.exe stays alive for the child's lifetime -- a small cost
// vs. the POSIX path where the wrapper is gone immediately.
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

// execChildWindows runs bin as a foreground subprocess, exits with
// the child's status code. Returns only on failure to spawn the
// child; on a clean child exit it calls os.Exit and never returns.
func execChildWindows(bin string, args, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

type fileFilter func(name string) bool

func allFiles(string) bool { return true }

func goSourceOnly(name string) bool {
	return strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum"
}

// goMajorMinor returns runtime.Version()'s "go1.26" prefix, stripping
// the patch component. Stable enough to bust the cache on minor
// upgrades (Go 1.26 -> 1.27) without falsely missing on routine
// patch differences between operator + CI Go installs.
func goMajorMinor() string {
	v := runtime.Version() // "go1.26.0", "go1.26.2", "devel ..."
	// Trim past the second dot.
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

func hashDirInto(h io.Writer, dir string, keep fileFilter) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "node_modules", ".git", ".claude-scratch", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !keep(name) {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		// Content-hash, not mtime+size: LOCAL-006 cross-machine
		// binary cache requires a reproducible key, and mtime
		// trivially diverges between operator + CI checkouts of
		// identical content. .sparkwing/ trees are tiny so the
		// extra read cost is negligible.
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		fileH := sha256.New()
		_, copyErr := io.Copy(fileH, f)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
		fmt.Fprintf(h, "%s\x00%x\n", rel, fileH.Sum(nil))
		return nil
	})
}

// localReplaceTargets returns absolute paths of every local-path
// replace directive in go.mod. Remote replaces are ignored (they're
// resolved via the module cache and change only on go.mod edits,
// which the go.mod hash already covers).
func localReplaceTargets(goModPath string) ([]string, error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return nil, err
	}
	mf, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(goModPath)
	var out []string
	for _, r := range mf.Replace {
		np := r.New.Path
		if np == "" || !isLocalPath(np) {
			continue
		}
		abs := np
		if !filepath.IsAbs(abs) {
			abs = filepath.Clean(filepath.Join(dir, np))
		}
		out = append(out, abs)
	}
	return out, nil
}

func isLocalPath(p string) bool {
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/")
}
