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

// ErrMiss is the sentinel for 404 from /bin/<hash>.
var ErrMiss = errors.New("remote binary cache: miss")

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
	// Temp + rename so partial downloads never leave a half-written
	// binary at the canonical path.
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
	// git refuses to write into a non-empty dir; wipe stale state.
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

// --- compile helpers ---

// SparkwingHome honors SPARKWING_HOME if set, otherwise ~/.sparkwing.
func SparkwingHome() string {
	if h := os.Getenv("SPARKWING_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sparkwing")
}

// CachedBinaryPath returns where a pipeline binary with the given
// hash lives.
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
// Recoverable by `go mod download`.
var ErrMissingGoSum = errors.New("missing go.sum entries")

// CompilePipeline `go build`s sparkwingDir -> dest. Stdout + stderr
// go to the parent's stderr; stderr is also tee'd into a buffer so we
// can detect the missing-go.sum case and return ErrMissingGoSum.
//
// If `.sparkwing/.resolved.mod` exists, compile is invoked with
// `-modfile=<path>` so the overlay's resolved versions take precedence
// over the git-tracked go.mod.
func CompilePipeline(sparkwingDir, dest string) error {
	// Bare exec.Command("go", ...) with no Go on PATH surfaces a
	// confusing message; preempt with a clearer one.
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
	var captured bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if strings.Contains(captured.String(), "missing go.sum entry") {
			return ErrMissingGoSum
		}
		return fmt.Errorf("compile .sparkwing/: %w", err)
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
func PipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	h := sha256.New()

	// Mix only major.minor of the Go runtime; patch releases don't
	// change generated-code ABI but commonly differ between operator
	// + CI installs.
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

	// Overlay modfile: when sparks are consumed via .resolved.mod
	// instead of local replaces, the replace-target walk doesn't
	// capture version bumps. Hash overlay contents directly.
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
// the patch component.
func goMajorMinor() string {
	v := runtime.Version() // "go1.26.0", "go1.26.2", "devel ..."
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
		// Content-hash, not mtime+size: cross-machine cache requires
		// a reproducible key (mtime diverges between checkouts).
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
// replace directive in go.mod. Remote replaces are ignored (the go.mod
// hash already covers them).
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
