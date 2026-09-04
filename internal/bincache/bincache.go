package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var ErrMiss = errors.New("remote binary cache: miss")

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

// SeedRef returns the only ref namespace accepted by the cache seed importer.
func SeedRef(sha string) string {
	return "refs/sparkwing-seed/" + sha
}

func CacheURL() string {
	return strings.TrimRight(os.Getenv("SPARKWING_GITCACHE_URL"), "/")
}

func CacheToken() string {
	return os.Getenv("SPARKWING_CACHE_TOKEN")
}

// ErrDigest reports a cached binary whose bytes do not match the digest the cache advertised,
// or a response that carried no digest at all.
var ErrDigest = errors.New("remote binary cache: digest verification failed")

func parseDigestHeader(value string) ([]byte, error) {
	for _, member := range strings.Split(value, ",") {
		name, encoded, ok := strings.Cut(strings.TrimSpace(member), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "sha-256") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("%w: undecodable sha-256 digest", ErrDigest)
		}
		if len(raw) != sha256.Size {
			return nil, fmt.Errorf("%w: sha-256 digest is %d bytes", ErrDigest, len(raw))
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%w: response carried no sha-256 digest", ErrDigest)
}

func TryBinary(gcURL, token, hash, dest string) error {
	req, err := http.NewRequest(http.MethodGet, gcURL+"/bin/"+hash, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cli := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
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
	want, err := parseDigestHeader(resp.Header.Get("Digest"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// safety: the key folds source inputs, not content, so only this digest ties the bytes to the cache entry.
	if !bytes.Equal(sum.Sum(nil), want) {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", ErrDigest, hash)
	}
	return os.Rename(tmp, dest)
}

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
	cli := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bin cache PUT %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if header := resp.Header.Get("Digest"); header != "" {
		stored, err := parseDigestHeader(header)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if !bytes.Equal(stored, sum[:]) {
			return fmt.Errorf("%w: cache stored different bytes for %s", ErrDigest, hash)
		}
	}
	return nil
}

func FetchPipelineSource(gcURL, repoSSH, branch, sha, parentDir string) (sparkwingDir string, err error) {
	return fetchPipelineSource(gcURL, "", repoSSH, branch, sha, parentDir, false, "")
}

// FetchPipelineSourceWithToken authenticates cache reads only when gcURL is the controller's proxy.
func FetchPipelineSourceWithToken(gcURL, controllerURL, token, repoSSH, branch, sha, parentDir string) (sparkwingDir string, err error) {
	return FetchPipelineSourceWithCredentials(gcURL, controllerURL, token, "", repoSSH, branch, sha, parentDir)
}

// FetchPipelineWorkspaceSourceWithToken materializes workspace blobs without checkout transformations.
func FetchPipelineWorkspaceSourceWithToken(gcURL, controllerURL, token, repoSSH, branch, sha, parentDir string) (sparkwingDir string, err error) {
	return FetchPipelineWorkspaceSourceWithCredentials(gcURL, controllerURL, token, "", repoSSH, branch, sha, parentDir)
}

// FetchPipelineSourceWithCredentials prevents a controller bearer from crossing into a direct cache origin.
func FetchPipelineSourceWithCredentials(
	gcURL, controllerURL, controllerToken, cacheToken, repoSSH, branch, sha, parentDir string,
) (sparkwingDir string, err error) {
	bearer := ControllerGitcacheToken(gcURL, controllerURL, controllerToken)
	if bearer == "" {
		bearer = cacheToken
	}
	return fetchPipelineSource(gcURL, bearer, repoSSH, branch, sha, parentDir, false,
		controllerClaimedRepoName(gcURL, controllerURL, repoSSH))
}

// FetchPipelineWorkspaceSourceWithCredentials combines raw workspace restoration with the same origin credential fence.
func FetchPipelineWorkspaceSourceWithCredentials(
	gcURL, controllerURL, controllerToken, cacheToken, repoSSH, branch, sha, parentDir string,
) (sparkwingDir string, err error) {
	bearer := ControllerGitcacheToken(gcURL, controllerURL, controllerToken)
	if bearer == "" {
		bearer = cacheToken
	}
	return fetchPipelineSource(gcURL, bearer, repoSSH, branch, sha, parentDir, true,
		controllerClaimedRepoName(gcURL, controllerURL, repoSSH))
}

// ControllerRunGitcacheURL turns the admin cache proxy into the claim-bound route used by node executors.
func ControllerRunGitcacheURL(gcURL, controllerURL, runID string) string {
	cache, cacheErr := parseCacheEndpoint(gcURL)
	controller, controllerErr := parseCacheEndpoint(controllerURL)
	if cacheErr != nil || controllerErr != nil || runID == "" ||
		!strings.EqualFold(cache.Scheme, controller.Scheme) ||
		!strings.EqualFold(cache.Host, controller.Host) ||
		strings.TrimRight(cache.Path, "/") != controllerGitcachePath(controller.Path) {
		return strings.TrimRight(gcURL, "/")
	}
	return strings.TrimRight(controllerURL, "/") + "/api/v1/runs/" + neturl.PathEscape(runID) + "/gitcache"
}

// ControllerGitcacheToken returns token only for the controller's exact cache-proxy origin and a reviewed proxy path.
func ControllerGitcacheToken(gcURL, controllerURL, token string) string {
	if token == "" {
		return ""
	}
	cache, cacheErr := parseCacheEndpoint(gcURL)
	controller, controllerErr := parseCacheEndpoint(controllerURL)
	if cacheErr != nil || controllerErr != nil ||
		!strings.EqualFold(cache.Scheme, controller.Scheme) ||
		!strings.EqualFold(cache.Host, controller.Host) {
		return ""
	}
	if !controllerGitcacheProxyPath(cache.Path, controller.Path) {
		return ""
	}
	return token
}

func controllerGitcachePath(controllerPath string) string {
	return strings.TrimRight(controllerPath, "/") + "/api/v1/gitcache"
}

func controllerGitcacheProxyPath(cachePath, controllerPath string) bool {
	path := strings.TrimRight(cachePath, "/")
	if path == controllerGitcachePath(controllerPath) {
		return true
	}
	return controllerRunGitcacheProxyPath(path, controllerPath)
}

func controllerRunGitcacheProxyPath(cachePath, controllerPath string) bool {
	path := strings.TrimRight(cachePath, "/")
	prefix := strings.TrimRight(controllerPath, "/") + "/api/v1/runs/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/gitcache") {
		return false
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/gitcache")
	return runID != "" && !strings.Contains(runID, "/")
}

func parseCacheEndpoint(raw string) (*neturl.URL, error) {
	u, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, errors.New("invalid cache endpoint")
	}
	return u, nil
}

func controllerClaimedRepoName(gcURL, controllerURL, repoURL string) string {
	cache, cacheErr := parseCacheEndpoint(gcURL)
	controller, controllerErr := parseCacheEndpoint(controllerURL)
	if cacheErr != nil || controllerErr != nil ||
		!strings.EqualFold(cache.Scheme, controller.Scheme) ||
		!strings.EqualFold(cache.Host, controller.Host) ||
		!controllerRunGitcacheProxyPath(cache.Path, controller.Path) {
		return ""
	}
	return ClaimedRepoNameFromURL(repoURL)
}

func fetchPipelineSource(gcURL, token, repoSSH, branch, sha, parentDir string, rawWorkspace bool, cacheName string) (sparkwingDir string, err error) {
	if gcURL == "" {
		return "", fmt.Errorf("FetchPipelineSource: SPARKWING_GITCACHE_URL not set")
	}
	if token == "" {
		token = CacheToken()
	}
	repoSSH, err = sourceurl.ValidateCloneURL(repoSSH)
	if err != nil {
		return "", fmt.Errorf("FetchPipelineSource: invalid repo URL: %w", err)
	}
	if branch == "" {
		branch = "main"
	}
	name := cacheName
	if name == "" {
		name = RepoNameFromURL(repoSSH)
	}
	if name == "" {
		return "", fmt.Errorf("FetchPipelineSource: cannot derive repo name from %q", repoSSH)
	}

	if err := registerRepoWithCache(gcURL, token, name, repoSSH); err != nil {
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
		if err := fetchExactSHA(gcURL, cloneURL, token, sha, workTree); err != nil {
			return "", err
		}
		workspaceCommit, inspectErr := isWorkspaceSnapshotCommit(workTree, sha)
		if inspectErr != nil {
			return "", inspectErr
		}
		if rawWorkspace || workspaceCommit {
			if err := restoreRawCheckout(workTree, sha); err != nil {
				return "", err
			}
		}
	} else {
		if err := shallowCloneBranch(gcURL, cloneURL, token, branch, workTree); err != nil {
			return "", err
		}
	}

	candidate := filepath.Join(workTree, ".sparkwing")
	if fi, statErr := os.Stat(candidate); statErr == nil && fi.IsDir() {
		return candidate, nil
	}
	return "", fmt.Errorf("cloned tree has no .sparkwing directory under %s", workTree)
}

func fetchExactSHA(gcURL, cloneURL, token, sha, dest string) error {
	// safety: the sha reaches git as a fetch argument, so only an object id may pass.
	sha, err := validateGitObject(sha)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	runIn := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dest
		cmd.Env = gitHTTPEnv(gcURL, token)
		return cmd.CombinedOutput()
	}
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", cloneURL},
		{"fetch", "--depth", "1", "--end-of-options", "origin", sha},
		{"-c", "core.attributesFile=/dev/null", "checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, step := range steps {
		if out, err := runIn(step...); err != nil {
			return fmt.Errorf("git %s (sha %s): %w: %s",
				strings.Join(step, " "), sha, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func isWorkspaceSnapshotCommit(repoDir, sha string) (bool, error) {
	out, err := exec.Command("git", "-C", repoDir, "cat-file", "commit", sha).Output()
	if err != nil {
		return false, fmt.Errorf("inspect workspace commit: %w", err)
	}
	header, message, ok := bytes.Cut(out, []byte("\n\n"))
	if !ok {
		return false, fmt.Errorf("inspect workspace commit: malformed identity")
	}
	parents := 0
	author := false
	for _, line := range bytes.Split(header, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("parent ")) {
			parents++
		}
		if bytes.HasPrefix(line, []byte("author Sparkwing <workspace@sparkwing.dev> ")) {
			author = true
		}
	}
	return author && parents == 0 && string(message) == "sparkwing working-tree snapshot\n", nil
}

func restoreRawCheckout(repoDir, sha string) error {
	out, err := exec.Command("git", "-C", repoDir, "ls-tree", "-rz", "--full-tree", sha).Output()
	if err != nil {
		return fmt.Errorf("inspect workspace tree: %w", err)
	}
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		metadata, name, ok := bytes.Cut(raw, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !ok || len(fields) != 3 || string(fields[1]) != "blob" {
			return fmt.Errorf("inspect workspace tree: malformed blob entry")
		}
		rel := filepath.Clean(filepath.FromSlash(string(name)))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("inspect workspace tree: unsafe path %q", name)
		}
		blob, blobErr := exec.Command("git", "-C", repoDir, "cat-file", "blob", string(fields[2])).Output()
		if blobErr != nil {
			return fmt.Errorf("read workspace blob %q: %w", name, blobErr)
		}
		path := filepath.Join(repoDir, rel)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("replace workspace path %q: %w", name, err)
		}
		switch string(fields[0]) {
		case "100644":
			err = os.WriteFile(path, blob, 0o644)
			if err == nil {
				err = os.Chmod(path, 0o644)
			}
		case "100755":
			err = os.WriteFile(path, blob, 0o755)
			if err == nil {
				err = os.Chmod(path, 0o755)
			}
		case "120000":
			err = os.Symlink(string(blob), path)
		default:
			return fmt.Errorf("workspace tree contains unsupported mode %s for %q", fields[0], name)
		}
		if err != nil {
			return fmt.Errorf("restore workspace path %q: %w", name, err)
		}
	}
	return nil
}

func shallowCloneBranch(gcURL, cloneURL, token, branch, dest string) error {
	cmd := exec.Command(
		"git", "clone",
		"--depth", "1",
		"--single-branch",
		"--branch", branch,
		cloneURL, dest,
	)
	cmd.Env = gitHTTPEnv(gcURL, token)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s (branch %s): %w: %s",
			cloneURL, branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func registerRepoWithCache(gcURL, token, name, repoURL string) error {
	q := neturl.Values{}
	q.Set("name", name)
	q.Set("repo", repoURL)
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(gcURL, "/")+"/git/register?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cli := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func gitHTTPEnv(gcURL, token string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	count, countIndex := 0, -1
	for i, value := range env {
		if strings.HasPrefix(value, "GIT_CONFIG_COUNT=") {
			_, _ = fmt.Sscanf(strings.TrimPrefix(value, "GIT_CONFIG_COUNT="), "%d", &count)
			countIndex = i
		}
	}
	entries := 1
	if token != "" {
		entries++
	}
	countValue := fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+entries)
	if countIndex >= 0 {
		env[countIndex] = countValue
	} else {
		env = append(env, countValue)
	}
	if token != "" {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s.extraHeader", count, strings.TrimRight(gcURL, "/")+"/"),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=Authorization: Bearer %s", count, token),
		)
		count++
	}
	env = append(env,
		fmt.Sprintf("GIT_CONFIG_KEY_%d=http.%s.followRedirects", count, strings.TrimRight(gcURL, "/")+"/"),
		fmt.Sprintf("GIT_CONFIG_VALUE_%d=false", count),
	)
	return env
}

func RefreshRepo(ctx context.Context, gcURL, token, repoURL string) error {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

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

	return SeedBundle(ctx, gcURL, token, repoURL, bundle, sha)
}

// SeedBundle imports a prebuilt Git bundle into the cache under sha.
func SeedBundle(ctx context.Context, gcURL, token, repoURL, bundle, sha string) error {
	if strings.TrimSpace(gcURL) == "" {
		return fmt.Errorf("SeedBundle: gitcache URL required")
	}
	return seedBundle(ctx, strings.TrimRight(gcURL, "/")+"/sync/seed", token, repoURL, bundle, sha, false)
}

// SeedWorkspaceBundle imports a bounded-retention working-tree bundle.
func SeedWorkspaceBundle(ctx context.Context, gcURL, token, repoURL, bundle, sha string) error {
	if strings.TrimSpace(gcURL) == "" {
		return fmt.Errorf("SeedWorkspaceBundle: gitcache URL required")
	}
	return seedBundle(ctx, strings.TrimRight(gcURL, "/")+"/sync/seed", token, repoURL, bundle, sha, true)
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

	ref := SeedRef(sha)
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
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

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

	return SeedBundleViaController(ctx, controllerURL, token, repoURL, bundle, sha)
}

// SeedBundleViaController imports a prebuilt Git bundle through the controller.
func SeedBundleViaController(ctx context.Context, controllerURL, token, repoURL, bundle, sha string) error {
	if strings.TrimSpace(controllerURL) == "" {
		return fmt.Errorf("SeedBundleViaController: controller URL required")
	}
	return seedBundle(ctx, strings.TrimRight(controllerURL, "/")+"/api/v1/gitcache/seed", token, repoURL, bundle, sha, false)
}

// SeedWorkspaceBundleViaController imports a bounded-retention working-tree bundle through the controller.
func SeedWorkspaceBundleViaController(ctx context.Context, controllerURL, token, repoURL, bundle, sha string) error {
	if strings.TrimSpace(controllerURL) == "" {
		return fmt.Errorf("SeedWorkspaceBundleViaController: controller URL required")
	}
	return seedBundle(ctx, strings.TrimRight(controllerURL, "/")+"/api/v1/gitcache/seed", token, repoURL, bundle, sha, true)
}

func seedBundle(ctx context.Context, endpoint, token, repoURL, bundle, sha string, workspace bool) error {
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return fmt.Errorf("seed bundle: invalid repo URL: %w", err)
	}
	sha, err = validateGitObject(sha)
	if err != nil {
		return err
	}
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 500<<20 {
		return fmt.Errorf("git bundle is %d bytes; limit is %d bytes", info.Size(), int64(500<<20))
	}
	q := neturl.Values{}
	q.Set("repo", repoURL)
	q.Set("sha", sha)
	if workspace {
		q.Set("workspace", "1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?"+q.Encode(), f)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func RepoNameFromURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	repoURL = strings.TrimSuffix(repoURL, "/")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	if i := strings.LastIndexAny(repoURL, "/:"); i >= 0 {
		return repoURL[i+1:]
	}
	return repoURL
}

// ClaimedRepoNameFromURL prevents equal basenames from sharing a claim-scoped cache authorization path.
func ClaimedRepoNameFromURL(repoURL string) string {
	if normalized, err := sourceurl.ValidateCloneURL(repoURL); err == nil {
		repoURL = normalized
	} else {
		repoURL = strings.TrimSpace(repoURL)
	}
	sum := sha256.Sum256([]byte(repoURL))
	return fmt.Sprintf("repo-%x", sum)[:64]
}

func RepoURLFromGitHub(fullName string) string {
	if fullName == "" {
		return ""
	}
	if strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return fullName
	}
	return "git@github.com:" + fullName + ".git"
}

func SparkwingHome() string {
	p, err := paths.DefaultPaths()
	if err != nil {
		return ".sparkwing"
	}
	return p.Root
}

var ErrMissingGoSum = errors.New("missing go.sum entries")

type CompileError struct {
	Output []byte
	Err    error
}

func (e *CompileError) Error() string { return fmt.Sprintf("compile .sparkwing/: %v", e.Err) }
func (e *CompileError) Unwrap() error { return e.Err }

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

func overlayModfilePath(sparkwingDir string) string {
	p := filepath.Join(sparkwingDir, ".resolved.mod")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

func goWorkInScope(sparkwingDir string) (string, bool) {
	switch env := os.Getenv("GOWORK"); env {
	case "off":
		return "", false
	case "":
	default:
		// #nosec G703 -- the GOWORK path comes from this process's own environment
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

func PipelineCacheKey(sparkwingDir string) (string, error) {
	return PipelineCacheKeyForPlatform(sparkwingDir, runtime.GOOS, runtime.GOARCH)
}

func PipelineCacheKeyForPlatform(sparkwingDir, goos, goarch string) (string, error) {
	parts, err := keyParts(sparkwingDir, goos, goarch)
	if err != nil {
		return "", err
	}
	return foldKey(parts), nil
}

type KeyPart struct {
	Label  string
	Digest string
	Detail string

	material []byte
}

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

func ExplainCacheKey(sparkwingDir string) (string, []KeyPart, error) {
	parts, err := keyParts(sparkwingDir, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", nil, err
	}
	return foldKey(parts), parts, nil
}

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

func ExecReplace(bin string, args []string, dir string, env []string) error {
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return err
		}
	}
	return execChild(bin, args, env)
}

type fileFilter func(name string) bool

func allFiles(string) bool { return true }

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

type HashStats struct {
	Files   int
	Bytes   int64
	Ignored int
}

func (s HashStats) String() string {
	base := fmt.Sprintf("%d files, %s", s.Files, humanSize(s.Bytes))
	if s.Ignored > 0 {
		base += fmt.Sprintf(" (%d gitignored, excluded)", s.Ignored)
	}
	return base
}

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

type replaceTarget struct {
	Label string
	Dir   string
}

func replaceLabel(old module.Version) string {
	if old.Version == "" {
		return old.Path
	}
	return old.Path + "@" + old.Version
}

func moduleLabelOf(dir string) (string, error) {
	p := filepath.Join(dir, "go.mod")
	// #nosec G703 -- the go.mod of a workspace module this process resolved
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
