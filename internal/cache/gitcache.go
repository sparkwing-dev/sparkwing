package cache

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var (
	gitcacheArchiveServed metric.Int64Counter
	gitcacheFileServed    metric.Int64Counter
	gitcacheFetchDur      metric.Float64Histogram
	gitcacheCacheHits     metric.Int64Counter
	gitcacheCacheMisses   metric.Int64Counter
	gitcacheRecoveryRecl  metric.Int64Counter
)

func initGitcacheMetrics() {
	meter := otelutil.Meter("sparkwing-cache")

	gitcacheArchiveServed, _ = meter.Int64Counter("sparkwing.gitcache.archives_served",
		metric.WithDescription("Total archives served"),
		metric.WithUnit("{archive}"))

	gitcacheFileServed, _ = meter.Int64Counter("sparkwing.gitcache.files_served",
		metric.WithDescription("Total files served"),
		metric.WithUnit("{file}"))

	gitcacheFetchDur, _ = meter.Float64Histogram("sparkwing.gitcache.fetch_duration",
		metric.WithDescription("Background git fetch duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2, 5, 10, 30, 60))

	gitcacheCacheHits, _ = meter.Int64Counter("sparkwing.gitcache.cache_hits",
		metric.WithDescription("Archive cache hits"),
		metric.WithUnit("{hit}"))

	gitcacheCacheMisses, _ = meter.Int64Counter("sparkwing.gitcache.cache_misses",
		metric.WithDescription("Archive cache misses"),
		metric.WithUnit("{miss}"))

	gitcacheRecoveryRecl, _ = meter.Int64Counter("sparkwing.gitcache.recovery_reclones",
		metric.WithDescription("Recovery reclones after a failed mirror fetch"),
		metric.WithUnit("{reclone}"))
}

var (
	proxyCacheHitsCounter   metric.Int64Counter
	proxyCacheMissesCounter metric.Int64Counter
	proxyUpstreamDuration   metric.Float64Histogram
)

func initProxyMetrics() {
	meter := otelutil.Meter("sparkwing-cache")
	proxyCacheHitsCounter, _ = meter.Int64Counter("sparkwing.proxy.cache_hits",
		metric.WithDescription("Proxy cache hits"),
		metric.WithUnit("{hit}"))
	proxyCacheMissesCounter, _ = meter.Int64Counter("sparkwing.proxy.cache_misses",
		metric.WithDescription("Proxy cache misses"),
		metric.WithUnit("{miss}"))
	proxyUpstreamDuration, _ = meter.Float64Histogram("sparkwing.proxy.upstream_duration",
		metric.WithDescription("Time to fetch from upstream"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10))
}

var validGitRef = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

const maxNegotiateCommits = 256

func validateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty git ref")
	}
	// safety: a ref that leads with a dash is read by git as an option, not a revision.
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("invalid git ref %q: begins with '-'", ref)
	}
	if !validGitRef.MatchString(ref) {
		return fmt.Errorf("invalid git ref %q: contains unsafe characters", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("invalid git ref %q: contains '..'", ref)
	}
	return nil
}

func short(ref string) string {
	if len(ref) <= 8 {
		return ref
	}
	return ref[:8]
}

var (
	dataRoot     = "/data"
	repoDir      = "/data/repos"
	archDir      = "/data/archives"
	artifactsDir = "/data/artifacts"
	binsDir      = "/data/bins"
	cacheDir     = "/data/cache"

	apiToken              string
	sshKeyDir             = "/etc/ssh-key"
	autoRegisterReposSpec string

	fetchFreshWindow = 15 * time.Second

	recloneCooldown = 1 * time.Hour

	workspaceSeedMaxAge = 24 * time.Hour

	repoLocks   = map[string]*sync.Mutex{}
	repoLocksMu sync.Mutex
)

func repoLock(key string) *sync.Mutex {
	repoLocksMu.Lock()
	defer repoLocksMu.Unlock()
	if _, ok := repoLocks[key]; !ok {
		repoLocks[key] = &sync.Mutex{}
	}
	return repoLocks[key]
}

func repoHash(repoURL string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(repoURL)))[:12]
}

func setupSSH() error {
	if _, err := os.Stat(sshKeyDir); err != nil {
		log.Printf("warning: no SSH key at %s -- only public repos will work", sshKeyDir)
		return nil
	}

	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")
	// safety: a key that cannot be staged makes every private-repo mirror fall back to no key, silently.
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("cache: stage SSH key: mkdir %s: %w", sshDir, err)
	}

	// hack: k8s secret mounts strip trailing newlines; OpenSSH requires keys end with one.
	entries, err := os.ReadDir(sshKeyDir)
	if err != nil {
		return fmt.Errorf("cache: stage SSH key: read %s: %w", sshKeyDir, err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(sshKeyDir, e.Name()))
		if err != nil {
			return fmt.Errorf("cache: stage SSH key %s: %w", e.Name(), err)
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		// #nosec G703 -- SSH keys staged from the operator's own mounted secret directory
		err = os.WriteFile(filepath.Join(sshDir, e.Name()), data, 0o600)
		if err != nil {
			return fmt.Errorf("cache: stage SSH key %s: %w", e.Name(), err)
		}
	}

	if err := os.Setenv("GIT_SSH_COMMAND", "ssh -i "+filepath.Join(sshDir, "id_ed25519")+" -o UserKnownHostsFile="+filepath.Join(sshDir, "known_hosts")+" -o StrictHostKeyChecking=yes"); err != nil {
		return fmt.Errorf("cache: stage SSH key: set GIT_SSH_COMMAND: %w", err)
	}
	log.Printf("SSH key configured from %s", sshKeyDir)
	return nil
}

func bearerToken(r *http.Request) string {
	scheme, rest, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	// safety: RFC 7235 makes the scheme case-insensitive, and a header with no scheme is not a credential.
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// safety: repointing a name redirects every clone of it, so a tokened cache demands the token. An open
// cache has one anonymous caller, and refusing there would make a squatted name permanent.
func mayRepoint(r *http.Request) bool {
	return apiToken == "" || authenticated(r)
}

func authenticated(r *http.Request) bool {
	if apiToken == "" {
		return false
	}
	got := bearerToken(r)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(apiToken)) == 1
}

func requireToken(next http.HandlerFunc) http.HandlerFunc {
	token := apiToken
	return func(w http.ResponseWriter, r *http.Request) {
		// safety: an empty token reaches here only when the operator asked for it at startup.
		if token == "" {
			next(w, r)
			return
		}
		got := bearerToken(r)
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			next(w, r)
			return
		}
		http.Error(w, "unauthorized -- set Authorization: Bearer <token> header", http.StatusUnauthorized)
	}
}

type fetchState struct {
	mu         sync.RWMutex
	repos      map[string]*repoFetchState
	allFailing bool
}

type repoFetchState struct {
	lastError   string
	lastErrorAt time.Time
	nextRetry   time.Time
	backoff     time.Duration
	lastOK      time.Time
	lastReclone time.Time
	reclones    []time.Time
}

var bgFetch = &fetchState{repos: map[string]*repoFetchState{}}

func stateKey(hash string) string { return hash + ".git" }

func (fs *fetchState) entry(name string) *repoFetchState {
	rs := fs.repos[name]
	if rs == nil {
		rs = &repoFetchState{}
		fs.repos[name] = rs
	}
	return rs
}

func (fs *fetchState) markFetched(name string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	rs := fs.entry(name)
	rs.lastOK = time.Now()
	rs.lastError = ""
	rs.backoff = 0

	rs.nextRetry = time.Time{}
}

func (fs *fetchState) fresh(name string) bool {
	if fetchFreshWindow <= 0 {
		return false
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rs := fs.repos[name]
	if rs == nil || rs.lastOK.IsZero() {
		return false
	}
	return time.Since(rs.lastOK) < fetchFreshWindow
}

func (fs *fetchState) allowReclone(name string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	rs := fs.entry(name)
	now := time.Now()
	if !rs.lastReclone.IsZero() && now.Sub(rs.lastReclone) < recloneCooldown {
		return false
	}
	rs.lastReclone = now
	kept := rs.reclones[:0]
	for _, t := range rs.reclones {
		if now.Sub(t) < 24*time.Hour {
			kept = append(kept, t)
		}
	}
	rs.reclones = append(kept, now)
	return true
}

func (fs *fetchState) recloneCooldownRemaining(name string) time.Duration {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	rs := fs.repos[name]
	if rs == nil || rs.lastReclone.IsZero() {
		return 0
	}
	left := recloneCooldown - time.Since(rs.lastReclone)
	if left < 0 {
		return 0
	}
	return left.Truncate(time.Second)
}

var mirrorFetch = func(timeout time.Duration, bareRepo string) (string, error) {
	return gitCmdTimeout(timeout, "-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")
}

var recloneMirror = func(repoURL, bareRepo string) (string, error) {
	_ = os.RemoveAll(bareRepo)
	return gitCmd("clone", "--bare", "--", repoURL, bareRepo)
}

const mirrorFetchTimeout = 2 * time.Minute

func fetchMirrorIfStale(hash, bareRepo string) (out string, skipped bool, err error) {
	name := stateKey(hash)
	if bgFetch.fresh(name) {
		return "", true, nil
	}
	out, err = mirrorFetch(mirrorFetchTimeout, bareRepo)
	if err == nil {
		bgFetch.markFetched(name)
	}
	return out, false, err
}

func refreshMirrorBestEffort(hash, bareRepo string) {
	if _, skipped, err := fetchMirrorIfStale(hash, bareRepo); err != nil {
		log.Printf("warning: mirror fetch for %s failed, serving cached refs: %v", hash, err)
	} else if skipped {
		log.Printf("mirror fetch for %s skipped (fetched within %s)", hash, fetchFreshWindow)
	}
}

func (fs *fetchState) problems() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var msgs []string
	if fs.allFailing {
		msgs = append(msgs, "All git fetches are failing -- SSH may be broken or the pod is resource-exhausted")
	}
	for name, rs := range fs.repos {
		repoName := strings.TrimSuffix(name, ".git")
		if recent := recentReclones(rs.reclones); recent > 1 {
			msgs = append(msgs, fmt.Sprintf(
				"repo %s: recovery reclone ran %d times in 24h -- persistent fetch failure; investigate the underlying git error; recloning on every archive request is expensive",
				repoName, recent))
		}
		if rs.lastError == "" {
			continue
		}
		if time.Since(rs.lastErrorAt) > 10*time.Minute {
			continue
		}
		msg := fmt.Sprintf("repo %s: %s", repoName, friendlyFetchError(rs.lastError))
		msgs = append(msgs, msg)
	}
	return msgs
}

func recentReclones(at []time.Time) int {
	n := 0
	for _, t := range at {
		if time.Since(t) < 24*time.Hour {
			n++
		}
	}
	return n
}

func friendlyFetchError(raw string) string {
	switch {
	case strings.Contains(raw, "cannot fork"):
		return "cannot fork SSH process -- pod is out of PIDs or memory"
	case strings.Contains(raw, "Permission denied"):
		return "SSH permission denied -- check that the SSH key has read access to this repo"
	case strings.Contains(raw, "Host key verification failed"):
		return "SSH host key verification failed -- known_hosts may be missing or stale"
	case strings.Contains(raw, "Could not resolve hostname"):
		return "DNS resolution failed -- check network connectivity"
	case strings.Contains(raw, "Connection refused"):
		return "SSH connection refused -- GitHub may be unreachable from this cluster"
	case strings.Contains(raw, "timed out"):
		return "git fetch timed out -- slow network or large repo"
	default:
		if len(raw) > 120 {
			return raw[:120] + "..."
		}
		return raw
	}
}

func backgroundFetchLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Printf("background fetch: every %s", interval)

	const maxBackoff = 10 * time.Minute
	consecutiveAllFail := 0

	for {
		if !sleepCtx(ctx, interval) {
			return
		}

		entries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}

		var fetched, failed int
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
				continue
			}

			bgFetch.mu.RLock()
			rs := bgFetch.repos[e.Name()]
			bgFetch.mu.RUnlock()
			if rs != nil && time.Now().Before(rs.nextRetry) {
				continue
			}

			bare := filepath.Join(repoDir, e.Name())
			mu := repoLock(bare)
			mu.Lock()
			fetchStart := time.Now()
			out, err := mirrorFetch(1*time.Minute, bare)
			mu.Unlock()
			if gitcacheFetchDur != nil {
				// safety: /metrics is unauthenticated, and a per-repo label enumerates the mirror set.
				gitcacheFetchDur.Record(context.Background(), time.Since(fetchStart).Seconds())
			}

			fetched++
			bgFetch.mu.Lock()
			if err != nil {
				failed++
				errMsg := strings.TrimSpace(fmt.Sprintf("%v %s", err, out))
				if rs == nil {
					rs = &repoFetchState{backoff: interval}
					bgFetch.repos[e.Name()] = rs
				} else {
					rs.backoff *= 2
					rs.backoff = min(rs.backoff, maxBackoff)
				}
				rs.lastError = errMsg
				rs.lastErrorAt = time.Now()
				rs.nextRetry = time.Now().Add(rs.backoff)
				bgFetch.mu.Unlock()
				log.Printf("background fetch: %s failed (retry in %s): %s", e.Name(), rs.backoff, errMsg)
			} else {

				rs = bgFetch.entry(e.Name())
				rs.lastError = ""
				rs.backoff = 0
				rs.lastOK = time.Now()
				bgFetch.mu.Unlock()
				log.Printf("background fetch: %s ok", e.Name())
			}
		}

		if fetched > 0 && failed == fetched {
			consecutiveAllFail++
			bgFetch.mu.Lock()
			bgFetch.allFailing = true
			bgFetch.mu.Unlock()
			pause := min(time.Duration(consecutiveAllFail)*interval, maxBackoff)
			log.Printf("background fetch: all %d repos failed -- pausing %s", failed, pause)
			if !sleepCtx(ctx, pause) {
				return
			}
		} else {
			consecutiveAllFail = 0
			bgFetch.mu.Lock()
			bgFetch.allFailing = false
			bgFetch.mu.Unlock()
		}
	}
}

func handleHealthCombined(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"status": "ok"}
	var problems []string

	if fetchProblems := bgFetch.problems(); len(fetchProblems) > 0 {
		problems = append(problems, fetchProblems...)
	}

	testPath := filepath.Join(proxyDir, ".health-check")
	if err := os.WriteFile(testPath, []byte("ok"), 0o644); err != nil {
		problems = append(problems, fmt.Sprintf("proxy: cache directory not writable: %v", err))
	} else {
		_ = os.Remove(testPath)
	}

	if len(problems) > 0 {
		resp["status"] = "degraded"
		resp["problems"] = problems
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleArchive(w http.ResponseWriter, r *http.Request) {
	repoURL := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
	if repoURL == "" || branch == "" {
		http.Error(w, "repo and branch required", http.StatusBadRequest)
		return
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		http.Error(w, "invalid repo URL: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateGitRef(branch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		// #nosec G706 -- the repository URL is validated at registration and redacted here
		log.Printf("background fetch: cloning %s → %s", sourceurl.Redact(repoURL), hash)
		if out, err := gitCmd("clone", "--bare", "--", repoURL, bareRepo); err != nil {
			http.Error(w, fmt.Sprintf("clone failed: %s\n%s", err, sshHint(out)), http.StatusInternalServerError)
			return
		}
		enableSHAFetch(bareRepo)
		bgFetch.markFetched(stateKey(hash))
	} else {
		enableSHAFetch(bareRepo)
		out, skipped, err := fetchMirrorIfStale(hash, bareRepo)
		switch {
		case skipped:
			log.Printf("archive: %s fetched within %s, serving mirror as-is", hash, fetchFreshWindow)
		case err != nil:

			if !bgFetch.allowReclone(stateKey(hash)) {
				left := bgFetch.recloneCooldownRemaining(stateKey(hash))
				log.Printf("archive: fetch failed for %s and recovery reclone is on cooldown (%s left): %v", hash, left, err)
				http.Error(w, fmt.Sprintf(
					"fetch failed: %s\n%s\nrecovery reclone is on cooldown for another %s -- a reclone already ran for this repo and the fetch is still failing.\n"+
						"This needs an operator: fix the git error above (a conflicting local ref after a branch rename is the usual cause, cleared with `git remote prune origin` / removing the conflicting ref in the mirror), because recloning the repository on every archive request costs a full download each time.",
					err, sshHint(out), left), http.StatusBadGateway)
				return
			}
			log.Printf("recovery reclone: %s -- fetch failed, recloning from origin (this re-downloads the whole repository): %v %s",
				hash, err, strings.TrimSpace(out))
			if gitcacheRecoveryRecl != nil {
				// safety: /metrics is unauthenticated, and a per-repo label enumerates the mirror set.
				gitcacheRecoveryRecl.Add(r.Context(), 1)
			}
			if recloneOut, err2 := recloneMirror(repoURL, bareRepo); err2 != nil {
				log.Printf("recovery reclone: %s failed: %v %s", hash, err2, strings.TrimSpace(recloneOut))
				http.Error(w, fmt.Sprintf("fetch failed: %s\n%s\nreclone also failed: %s %s", err, sshHint(out), err2, recloneOut), http.StatusInternalServerError)
				return
			}
			enableSHAFetch(bareRepo)
			bgFetch.markFetched(stateKey(hash))
			log.Printf("recovery reclone: %s succeeded", hash)
		default:
			log.Printf("archive: fetched %s", hash)
		}
	}

	// #nosec G702 -- git runs as argv with a constant binary and a ref that cannot begin with a dash
	commitBytes, err := exec.Command("git", "-C", bareRepo, "rev-parse", "--verify", "--end-of-options", branch).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("branch %q not found", branch), http.StatusNotFound)
		return
	}
	commit := strings.TrimSpace(string(commitBytes))
	// safety: this becomes a path component, so it must be a git object id and nothing else git printed.
	if !gitObjectRE.MatchString(commit) {
		http.Error(w, fmt.Sprintf("unexpected commit hash for branch %q: %q", branch, commit), http.StatusInternalServerError)
		return
	}
	shortCommit := short(commit)

	tarball := filepath.Join(archDir, hash+"-"+shortCommit+".tar.gz")
	// #nosec G703 -- the archive name is a repository hash plus a hex-validated commit prefix
	_, err = os.Stat(tarball)
	if err == nil {
		// #nosec G706 -- the repository hash and the short commit are pattern-validated
		log.Printf("cache hit: %s@%s", hash, shortCommit)
		if gitcacheCacheHits != nil {
			gitcacheCacheHits.Add(r.Context(), 1)
		}
		if gitcacheArchiveServed != nil {
			gitcacheArchiveServed.Add(r.Context(), 1)
		}
		serveTarball(w, r, tarball, hash, commit)
		return
	}

	if gitcacheCacheMisses != nil {
		gitcacheCacheMisses.Add(r.Context(), 1)
	}

	// #nosec G706 -- the repository hash and the short commit are pattern-validated
	log.Printf("cache hit: archiving %s@%s", hash, shortCommit)
	tmpTar := tarball + ".tmp"
	if err := archiveToFile(bareRepo, branch, tmpTar); err != nil {
		// #nosec G703 -- a temporary name beside the hex-validated archive path this handler built
		_ = os.Remove(tmpTar)
		http.Error(w, fmt.Sprintf("archive failed: %s", err), http.StatusInternalServerError)
		return
	}
	// #nosec G703 -- a temporary name beside the hex-validated archive path this handler built
	err = os.Rename(tmpTar, tarball)
	if err != nil {
		// #nosec G703 -- a temporary name beside the hex-validated archive path this handler built
		_ = os.Remove(tmpTar)
		http.Error(w, fmt.Sprintf("rename archive failed: %s", err), http.StatusInternalServerError)
		return
	}

	cleanOldArchives(hash)

	serveTarball(w, r, tarball, hash, commit)
}

func serveTarball(w http.ResponseWriter, r *http.Request, path, hash, commit string) {
	w.Header().Set("X-Commit", commit)
	w.Header().Set("X-Repo-Hash", hash)
	// #nosec G703 -- the archive name is a repository hash plus a hex-validated commit prefix
	http.ServeFile(w, r, path)
}

func handleRepos(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(repoDir)
	type repoInfo struct {
		Hash string `json:"hash"`
		Size int64  `json:"size_bytes"`
	}
	var repos []repoInfo
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".git") {
			info, _ := e.Info()
			repos = append(repos, repoInfo{
				Hash: strings.TrimSuffix(e.Name(), ".git"),
				Size: info.Size(),
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(repos)
}

func sshHint(output string) string {
	if strings.Contains(output, "Permission denied") || strings.Contains(output, "Host key verification failed") {
		return "hint: SSH key rejected -- run: sparkwing cluster update-ssh-key --name <cluster> --github-ssh-key ~/.ssh/<your-key>"
	}
	return ""
}

func gitCmd(args ...string) (string, error) {
	return gitCmdTimeout(2*time.Minute, args...)
}

func enableSHAFetch(bareRepo string) {
	if out, err := gitCmd("-C", bareRepo, "config",
		"uploadpack.allowReachableSHA1InWant", "true"); err != nil {
		log.Printf("warning: enableSHAFetch on %s failed: %v %s", bareRepo, err, out)
	}
}

var gitForkSem = make(chan struct{}, 4)

func gitCmdTimeout(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case gitForkSem <- struct{}{}:
		defer func() { <-gitForkSem }()
	case <-ctx.Done():
		return "", fmt.Errorf("git timed out waiting for fork slot (%d in flight): git %s",
			cap(gitForkSem), strings.Join(args, " "))
	}

	// #nosec G702 -- git runs as argv with a constant binary and pattern-validated refs
	cmd := exec.CommandContext(ctx, "git", args...)
	// safety: process group kill prevents SSH child orphans on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("git timed out after %s: git %s", timeout, strings.Join(args, " "))
	}
	return string(out), err
}

func archiveToFile(bareRepo, branch, outPath string) error {
	// #nosec G702 -- git runs as argv with a constant binary and pattern-validated refs
	gitArchive := exec.Command("git", "-C", bareRepo, "archive", "--format=tar", "--", branch)
	gzipCmd := exec.Command("gzip")

	pipe, err := gitArchive.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe setup: %w", err)
	}
	gzipCmd.Stdin = pipe

	// #nosec G703 -- an output path this handler built from the hex-validated archive name
	outFile, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer outFile.Close()
	gzipCmd.Stdout = outFile

	if err := gitArchive.Start(); err != nil {
		return fmt.Errorf("git archive start: %w", err)
	}
	if err := gzipCmd.Start(); err != nil {
		_ = gitArchive.Process.Kill()
		return fmt.Errorf("gzip start: %w", err)
	}

	if err := gitArchive.Wait(); err != nil {
		_ = gzipCmd.Process.Kill()
		return fmt.Errorf("git archive: %w", err)
	}
	if err := gzipCmd.Wait(); err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	return nil
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	repoURL := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
	filePath := r.URL.Query().Get("path")
	if repoURL == "" || branch == "" || filePath == "" {
		http.Error(w, "repo, branch, and path required", http.StatusBadRequest)
		return
	}
	if err := validateGitRef(branch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		http.Error(w, "repo not cached -- trigger an archive first", http.StatusNotFound)
		return
	}

	refreshMirrorBestEffort(hash, bareRepo)

	// #nosec G702 -- git runs as argv with a constant binary and a validated ref leading the object name
	out, err := exec.Command("git", "-C", bareRepo, "show", "--end-of-options", branch+":"+filePath).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("file not found: %s:%s", branch, filePath), http.StatusNotFound)
		return
	}

	if gitcacheFileServed != nil {
		gitcacheFileServed.Add(r.Context(), 1)
	}

	w.Header().Set("Content-Type", "text/plain")
	// #nosec G705 -- the response is text/plain, never HTML
	w.Write(out)
}

func handleTreeHash(w http.ResponseWriter, r *http.Request) {
	repoURL := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
	path := r.URL.Query().Get("path")
	if repoURL == "" || branch == "" {
		http.Error(w, "repo and branch required", http.StatusBadRequest)
		return
	}
	if err := validateGitRef(branch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		http.Error(w, "repo not cached", http.StatusNotFound)
		return
	}

	refreshMirrorBestEffort(hash, bareRepo)

	ref := branch
	if path != "" {
		ref = branch + ":" + path
	}

	// #nosec G702 -- git runs as argv with a constant binary and a ref that cannot begin with a dash
	out, err := exec.Command("git", "-C", bareRepo, "rev-parse", "--verify", "--end-of-options", ref).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("path not found: %s", ref), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	// #nosec G705 -- the response is text/plain, never HTML
	w.Write([]byte(strings.TrimSpace(string(out))))
}

func handleBranchContains(w http.ResponseWriter, r *http.Request) {
	repoURL := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
	commit := r.URL.Query().Get("commit")
	if repoURL == "" || branch == "" || commit == "" {
		http.Error(w, "repo, branch, and commit required", http.StatusBadRequest)
		return
	}
	if err := validateGitRef(branch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateGitRef(commit); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		http.Error(w, "repo not cached", http.StatusNotFound)
		return
	}

	refreshMirrorBestEffort(hash, bareRepo)

	// #nosec G702 -- git runs as argv with a constant binary and refs that cannot begin with a dash
	err := exec.Command("git", "-C", bareRepo, "merge-base", "--is-ancestor", "--", commit, branch).Run()
	if err != nil {
		http.Error(w, fmt.Sprintf("commit %s is not on branch %s", commit, branch), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- the ref and the commit are pattern-validated and the body is not HTML
	fmt.Fprintf(w, "commit %s is on branch %s", commit, branch)
}

var validBinHash = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{8}){0,3}(\.sha256)?$`)

type binMeta struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Principal string `json:"principal"`
	WrittenAt string `json:"written_at"`
}

func binMetaPath(hash string) string { return filepath.Join(binsDir, hash+".meta.json") }

func writingPrincipal(r *http.Request) string {
	token := bearerToken(r)
	if token == "" {
		return "anonymous"
	}
	// safety: fingerprint the bearer so attribution never persists the credential itself.
	sum := sha256.Sum256([]byte(token))
	return "token:" + hex.EncodeToString(sum[:])[:12]
}

func readBinMeta(hash string) (binMeta, error) {
	var meta binMeta
	data, err := os.ReadFile(binMetaPath(hash))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	if _, err := hex.DecodeString(meta.SHA256); err != nil || len(meta.SHA256) != 64 {
		return binMeta{}, fmt.Errorf("bin meta %s: malformed digest", hash)
	}
	return meta, nil
}

func writeBinMeta(hash string, meta binMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(binsDir, "meta-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, binMetaPath(hash)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func createBinMeta(hash string, meta binMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(binMetaPath(hash), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(binMetaPath(hash))
		return err
	}
	return f.Close()
}

var binKeyLocks sync.Map

func binKeyLock(hash string) *sync.Mutex {
	mu, _ := binKeyLocks.LoadOrStore(hash, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func binDigest(hash string, f *os.File) (string, error) {
	if meta, err := readBinMeta(hash); err == nil {
		return meta.SHA256, nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	meta := binMeta{SHA256: digest, Size: info.Size(), Principal: "unknown", WrittenAt: info.ModTime().UTC().Format(time.RFC3339)}
	// safety: never replace a sidecar an upload wrote while this read was hashing the older blob.
	if err := createBinMeta(hash, meta); err != nil && !errors.Is(err, fs.ErrExist) {
		// #nosec G706 -- the blob hash is pattern-validated
		log.Printf("warning: bin meta write %s: %v", hash, err)
	}
	return digest, nil
}

func setBinDigestHeaders(w http.ResponseWriter, digest string) error {
	raw, err := hex.DecodeString(digest)
	if err != nil {
		return err
	}
	w.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(raw))
	w.Header().Set("ETag", `"`+digest+`"`)
	return nil
}

func openBinForRead(hash, path string) (*os.File, string, error) {
	// safety: an upload replaces the blob by rename, so the fd and its digest must be taken under one lock.
	mu := binKeyLock(hash)
	mu.Lock()
	defer mu.Unlock()
	// #nosec G703 -- the blob path is built from a pattern-validated hash
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	digest, err := binDigest(hash, f)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, digest, nil
}

func stageBinBlob(data []byte) (string, error) {
	tmp, err := os.CreateTemp(binsDir, "bin-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

func handleBin(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/bin/")
	if !validBinHash.MatchString(hash) {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}

	path := filepath.Join(binsDir, hash)

	switch r.Method {
	case http.MethodGet:
		f, digest, err := openBinForRead(hash, path)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin digest %s: %v", hash, err)
			http.Error(w, "digest unavailable", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if err := setBinDigestHeaders(w, digest); err != nil {
			http.Error(w, "digest unavailable", http.StatusInternalServerError)
			return
		}
		info, _ := f.Stat()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		if _, err := io.Copy(w, f); err != nil {
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin copy %s: %v", hash, err)
		}

	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		principal := writingPrincipal(r)

		mu := binKeyLock(hash)
		mu.Lock()
		defer mu.Unlock()

		tmpPath, err := stageBinBlob(data)
		if err != nil {
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin stage %s: %v", hash, err)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		// safety: record the digest before the blob so a torn write serves a mismatch the client discards.
		meta := binMeta{SHA256: digest, Size: int64(len(data)), Principal: principal, WrittenAt: time.Now().UTC().Format(time.RFC3339)}
		if err := writeBinMeta(hash, meta); err != nil {
			_ = os.Remove(tmpPath)
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin meta write %s: %v", hash, err)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		// #nosec G703 -- the blob path is built from a pattern-validated hash
		err = os.Rename(tmpPath, path)
		if err != nil {
			_ = os.Remove(tmpPath)
			// #nosec G703 -- a pattern-validated hash; a digest with no blob would brick every later read
			_ = os.Remove(binMetaPath(hash))
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin write %s: %v", hash, err)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		// #nosec G706 -- the blob hash is pattern-validated
		log.Printf("bin cache: stored %s (%d bytes) sha256=%s principal=%s", hash, len(data), digest, principal)
		if err := setBinDigestHeaders(w, digest); err != nil {
			http.Error(w, "digest unavailable", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "GET or PUT only", http.StatusMethodNotAllowed)
	}
}

var validCacheKey = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

func handleCache(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/cache/")
	if !validCacheKey.MatchString(key) {
		http.Error(w, "invalid cache key: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}

	path := filepath.Join(cacheDir, key+".tar.gz")

	switch r.Method {
	case http.MethodHead:
		// #nosec G703 -- the blob path is built from a pattern-validated hash
		_, err := os.Stat(path)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		// #nosec G703 -- the blob path is built from a pattern-validated hash
		f, err := os.Open(path)
		if err != nil {
			if gitcacheCacheMisses != nil {
				gitcacheCacheMisses.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("type", "dependency"),
				))
			}
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		if gitcacheCacheHits != nil {
			gitcacheCacheHits.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("type", "dependency"),
			))
		}
		// #nosec G706 -- the cache key is pattern-validated
		log.Printf("cache hit: %s (%d bytes)", key, info.Size())
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		if _, err := io.Copy(w, f); err != nil {
			// #nosec G706 -- the cache key is pattern-validated
			log.Printf("warning: cache copy %s: %v", key, err)
		}

	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 500<<20)

		tmpFile, err := os.CreateTemp(cacheDir, "upload-*.tmp")
		if err != nil {
			http.Error(w, "failed to create temp file", http.StatusInternalServerError)
			return
		}
		tmpPath := tmpFile.Name()

		n, err := io.Copy(tmpFile, r.Body)
		tmpFile.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		// #nosec G703 -- the cache key is pattern-validated
		err = os.Rename(tmpPath, path)
		if err != nil {
			_ = os.Remove(tmpPath)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		// #nosec G706 -- the cache key is pattern-validated
		log.Printf("cache store: %s (%d bytes)", key, n)
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "GET, HEAD, or PUT only", http.StatusMethodNotAllowed)
	}
}

const workspaceRefPrefix = "refs/sparkwing-workspace/"

const (
	workspaceArchiveRefPrefix = "refs/sparkwing-workspace-archive/"
	workspaceArchiveAgeFactor = 7
)

var validJobID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func handleArtifacts(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "job ID required: /artifacts/{jobID}", http.StatusBadRequest)
		return
	}
	jobID := parts[0]
	// safety: the path is decoded here, so a job ID that is not one segment escapes the root.
	if jobID == "." || jobID == ".." || !validJobID.MatchString(jobID) {
		http.Error(w, "invalid job ID: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		artifactUpload(w, r, jobID)
	case http.MethodGet:
		if r.URL.Query().Has("glob") {
			artifactDownload(w, r, jobID)
		} else {
			artifactList(w, r, jobID)
		}
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func artifactUpload(w http.ResponseWriter, r *http.Request, jobID string) {
	artifactPath := r.URL.Query().Get("path")
	if artifactPath == "" {
		http.Error(w, "path query param required", http.StatusBadRequest)
		return
	}

	artifactPath = filepath.Clean(artifactPath)
	if strings.Contains(artifactPath, "..") || filepath.IsAbs(artifactPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	// safety: bsdtar reads a member name that leads with @ as an archive to inline, and -- does not stop it.
	for seg := range strings.SplitSeq(artifactPath, string(filepath.Separator)) {
		if strings.HasPrefix(seg, "@") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
	}

	jobDir := filepath.Join(artifactsDir, jobID)
	dest := filepath.Join(jobDir, artifactPath)
	absRoot, _ := filepath.Abs(artifactsDir)
	absDest, _ := filepath.Abs(dest)
	// safety: contain against the artifacts root, not the job directory a job ID could have moved.
	if !strings.HasPrefix(absDest, absRoot+string(filepath.Separator)) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	destDir := filepath.Dir(dest)
	// #nosec G703 -- the destination is contained under the artifacts root
	err := os.MkdirAll(destDir, 0o755)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// #nosec G703 -- the destination is contained under the artifacts root
	f, err := os.Create(dest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// #nosec G706 -- %q escapes control characters in the caller-supplied path
	log.Printf("describe: artifact uploaded %s/%q (%d bytes)", jobID, artifactPath, n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"path": artifactPath, "size": n})
}

func artifactDownload(w http.ResponseWriter, r *http.Request, jobID string) {
	glob := r.URL.Query().Get("glob")
	jobDir := filepath.Join(artifactsDir, jobID)

	// #nosec G703 -- the job directory is built from a pattern-validated job ID
	_, err := os.Stat(jobDir)
	if os.IsNotExist(err) {
		http.Error(w, "no artifacts for job "+jobID, http.StatusNotFound)
		return
	}

	var matches []string
	collect := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(jobDir, path)
		if matched, _ := filepath.Match(glob, filepath.Base(rel)); matched {
			matches = append(matches, rel)
		}
		if matched, _ := filepath.Match(glob, rel); matched && !contains(matches, rel) {
			matches = append(matches, rel)
		}
		return nil
	}
	// #nosec G703 -- the job directory is built from a pattern-validated job ID
	if err := filepath.Walk(jobDir, collect); err != nil {
		http.Error(w, fmt.Sprintf("walk artifacts: %s", err), http.StatusInternalServerError)
		return
	}

	if len(matches) == 0 {
		http.Error(w, fmt.Sprintf("no artifacts matching %q for job %s", glob, jobID), http.StatusNotFound)
		return
	}

	if len(matches) == 1 {
		// safety: an artifact is caller-supplied content, so it downloads instead of rendering in place.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", attachmentDisposition(filepath.Base(matches[0])))
		// #nosec G703 -- the match came from walking the job directory itself
		http.ServeFile(w, r, filepath.Join(jobDir, matches[0]))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(jobID+".tar"))
	// #nosec G702 -- tar runs as argv with a constant binary and -- ends its option list
	cmd := exec.Command("tar", append([]string{"-cf", "-", "-C", jobDir, "--"}, matches...)...)
	cmd.Stdout = w
	if err := cmd.Run(); err != nil {
		// #nosec G706 -- the job ID is pattern-validated
		log.Printf("warning: tar artifacts for %s: %v", jobID, err)
	}
}

func attachmentDisposition(name string) string {
	return "attachment; filename=" + strconv.Quote(strings.ReplaceAll(name, `"`, ""))
}

func artifactList(w http.ResponseWriter, r *http.Request, jobID string) {
	jobDir := filepath.Join(artifactsDir, jobID)

	// #nosec G703 -- the job directory is built from a pattern-validated job ID
	_, err := os.Stat(jobDir)
	if os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]string{})
		return
	}

	var files []string
	collect := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(jobDir, path)
		files = append(files, rel)
		return nil
	}
	// #nosec G703 -- the job directory is built from a pattern-validated job ID
	if err := filepath.Walk(jobDir, collect); err != nil {
		http.Error(w, fmt.Sprintf("walk artifacts: %s", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(files)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

var uploadsDir = "/data/uploads"

var validUploadID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	data, err := io.ReadAll(io.LimitReader(r.Body, 500<<20))
	if err != nil {
		http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	repoURL := r.URL.Query().Get("repo")
	base := r.URL.Query().Get("base")

	if base != "" && repoURL != "" {
		id, size, err := handleIncrementalUpload(data, repoURL, base)
		if err != nil {
			log.Printf("warning: incremental upload failed, storing as-is: %v", err)
		} else {
			log.Printf("describe: upload %s (incremental from %s, %d bytes)", id, short(base), size)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "size": size})
			return
		}
	}

	id := fmt.Sprintf("%x", sha256.Sum256(data))[:16]
	path := filepath.Join(uploadsDir, id+".tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("describe: upload %s (%d bytes)", id, len(data))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "size": len(data)})
}

func handleIncrementalUpload(diffData []byte, repoURL, base string) (string, int, error) {
	if err := validateGitRef(base); err != nil {
		return "", 0, fmt.Errorf("invalid base ref: %w", err)
	}

	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		return "", 0, fmt.Errorf("repo not cached: %s", hash)
	}

	workDir, err := os.MkdirTemp("", "sparkwing-incremental-*")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	if err := archiveToDir(bareRepo, base, workDir); err != nil {
		return "", 0, fmt.Errorf("checkout base %s: %w", short(base), err)
	}

	tmpDiff, err := os.CreateTemp("", "sparkwing-diff-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(tmpDiff.Name()) }()
	if _, err := tmpDiff.Write(diffData); err != nil {
		tmpDiff.Close()
		return "", 0, fmt.Errorf("write diff: %w", err)
	}
	tmpDiff.Close()

	cmd := exec.Command("tar", "-xzf", tmpDiff.Name(), "-C", workDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("extract diff: %s: %w", string(out), err)
	}

	tmpCombined, err := os.CreateTemp("", "sparkwing-combined-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = os.Remove(tmpCombined.Name()) }()
	tmpCombined.Close()

	tarCmd := exec.Command("tar", "-czf", tmpCombined.Name(), "-C", workDir, ".")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("create combined tarball: %s: %w", string(out), err)
	}

	combined, err := os.ReadFile(tmpCombined.Name())
	if err != nil {
		return "", 0, err
	}

	id := fmt.Sprintf("%x", sha256.Sum256(combined))[:16]
	path := filepath.Join(uploadsDir, id+".tar.gz")
	// #nosec G703 -- the upload name is a sha256 digest of the bundle
	err = os.WriteFile(path, combined, 0o644)
	if err != nil {
		return "", 0, err
	}

	return id, len(combined), nil
}

func archiveToDir(bareRepo, ref, dir string) error {
	// #nosec G702 -- git runs as argv with a constant binary and pattern-validated refs
	gitArchive := exec.Command("git", "-C", bareRepo, "archive", "--format=tar", "--", ref)
	tarExtract := exec.Command("tar", "-xf", "-", "-C", dir)

	pipe, err := gitArchive.StdoutPipe()
	if err != nil {
		return err
	}
	tarExtract.Stdin = pipe

	if err := gitArchive.Start(); err != nil {
		return err
	}
	if err := tarExtract.Start(); err != nil {
		_ = gitArchive.Process.Kill()
		return err
	}
	if err := gitArchive.Wait(); err != nil {
		_ = tarExtract.Process.Kill()
		return err
	}
	return tarExtract.Wait()
}

func handleUploadDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/uploads/")
	id = strings.TrimSuffix(id, ".tar.gz")
	if id == "" {
		http.Error(w, "upload ID required", http.StatusBadRequest)
		return
	}
	// safety: the path is decoded here, so an id that is not one segment escapes the uploads root.
	if id == "." || id == ".." || !validUploadID.MatchString(id) {
		http.Error(w, "invalid upload ID: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}

	path := filepath.Join(uploadsDir, id+".tar.gz")
	// #nosec G703 -- the upload id is pattern-validated to a single path segment
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		http.Error(w, "upload not found: "+id, http.StatusNotFound)
		return
	}

	// #nosec G703 -- ServeFile rejects a request path that contains a dot-dot element
	http.ServeFile(w, r, path)
}

func handleSyncNegotiate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Repo    string   `json:"repo"`
		Commits []string `json:"commits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Repo == "" || len(req.Commits) == 0 {
		http.Error(w, "repo and commits required", http.StatusBadRequest)
		return
	}
	// safety: every commit costs one git fork, so an unbounded list is a free fork bomb.
	if len(req.Commits) > maxNegotiateCommits {
		http.Error(w, fmt.Sprintf("at most %d commits per negotiate request", maxNegotiateCommits), http.StatusBadRequest)
		return
	}
	for _, commit := range req.Commits {
		// safety: each commit reaches git as a revision argument, so only an object id may pass.
		if !gitObjectRE.MatchString(commit) {
			http.Error(w, "each commit must be a 40-64 character hex object id", http.StatusBadRequest)
			return
		}
	}

	hash := repoHash(req.Repo)
	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ancestor": "", "found": false})
		return
	}

	lock := repoLock(hash)
	lock.Lock()
	refreshMirrorBestEffort(hash, bareRepo)
	lock.Unlock()

	for _, commit := range req.Commits {
		err := exec.Command("git", "-C", bareRepo, "cat-file", "-t", "--", commit).Run()
		if err == nil {
			log.Printf("sync negotiate: found common ancestor %s for %s", short(commit), hash)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ancestor": commit, "found": true})
			return
		}
	}

	log.Printf("sync negotiate: no common ancestor for %s (%d commits checked)", hash, len(req.Commits))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ancestor": "", "found": false})
}

func handleSyncSeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	repoURL := r.URL.Query().Get("repo")
	if repoURL == "" {
		http.Error(w, "repo query param required", http.StatusBadRequest)
		return
	}
	// safety: this URL becomes the mirror's origin, which a later request fetches.
	validRepoURL, err := sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		http.Error(w, "invalid repo URL: "+err.Error(), http.StatusBadRequest)
		return
	}
	repoURL = validRepoURL
	sha := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sha")))
	if !gitObjectRE.MatchString(sha) {
		http.Error(w, "sha query param must be a 40-64 character hex object id", http.StatusBadRequest)
		return
	}
	workspace := r.URL.Query().Get("workspace") == "1"

	tmpBundle, err := os.CreateTemp("", "seed-*.bundle")
	if err != nil {
		http.Error(w, "temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.Remove(tmpBundle.Name()) }()
	body := http.MaxBytesReader(w, r.Body, 500<<20)
	size, err := io.Copy(tmpBundle, body)
	if closeErr := tmpBundle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		http.Error(w, "read bundle: "+err.Error(), http.StatusInternalServerError)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")
	bundleRef := bincache.SeedRef(sha)
	seedRef := bundleRef
	if workspace {
		seedRef = "refs/sparkwing-workspace-incoming/" + sha
	}

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		// #nosec G706 -- the repository hash and the commit are pattern-validated
		log.Printf("seed: creating bare repo from bundle for %s at %s", hash, short(sha))
		if out, err := gitCmd("init", "--bare", bareRepo); err != nil {
			http.Error(w, fmt.Sprintf("init bare repo failed: %s\n%s", err, out), http.StatusInternalServerError)
			return
		}
		if out, err := gitCmd("-C", bareRepo, "fetch", tmpBundle.Name(), bundleRef+":"+seedRef); err != nil {
			_ = os.RemoveAll(bareRepo)
			http.Error(w, fmt.Sprintf("fetch seed ref failed: %s\n%s", err, out), http.StatusBadRequest)
			return
		}
		_, _ = gitCmd("-C", bareRepo, "remote", "set-url", "origin", repoURL)
		enableSHAFetch(bareRepo)
	} else {
		enableSHAFetch(bareRepo)
		// #nosec G706 -- the repository hash and the commit are pattern-validated
		log.Printf("seed: updating bare repo from bundle for %s at %s", hash, short(sha))
		if out, err := gitCmd("-C", bareRepo, "fetch", tmpBundle.Name(), bundleRef+":"+seedRef); err != nil {
			http.Error(w, fmt.Sprintf("fetch seed ref failed: %s\n%s", err, out), http.StatusBadRequest)
			return
		}
	}
	if out, err := gitCmd("-C", bareRepo, "cat-file", "-e", sha+"^{commit}"); err != nil {
		if workspace {
			_, _ = gitCmd("-C", bareRepo, "update-ref", "-d", seedRef)
			pruneUnreachableSeedObjects(bareRepo)
		}
		http.Error(w, fmt.Sprintf("seeded commit missing: %s\n%s", err, out), http.StatusBadRequest)
		return
	}
	if workspace {
		if err := retainWorkspaceSeed(bareRepo, seedRef, sha, 128, workspaceSeedMaxAge); err != nil {
			_, _ = gitCmd("-C", bareRepo, "update-ref", "-d", seedRef)
			pruneUnreachableSeedObjects(bareRepo)
			http.Error(w, "retain workspace seed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	pruneUnreachableSeedObjects(bareRepo)

	// #nosec G706 -- the repository hash and the commit are pattern-validated
	log.Printf("seed: %s seeded %s (%d bytes)", hash, short(sha), size)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "size": size})
}

type workspaceRef struct {
	name   string
	object string
}

func listWorkspaceRefs(bareRepo, prefix string) ([]workspaceRef, error) {
	out, err := gitCmd("-C", bareRepo, "for-each-ref", "--format=%(refname) %(objectname)", "--sort=-refname", prefix)
	if err != nil {
		return nil, fmt.Errorf("list %s refs: %w: %s", prefix, err, out)
	}
	var refs []workspaceRef
	for _, line := range strings.Split(out, "\n") {
		name, object, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name == "" || object == "" {
			continue
		}
		refs = append(refs, workspaceRef{name: name, object: object})
	}
	return refs, nil
}

func retainWorkspaceSeed(bareRepo, seedRef, sha string, limit int, maxAge time.Duration) error {
	refs, err := listWorkspaceRefs(bareRepo, workspaceRefPrefix)
	if err != nil {
		return err
	}
	kept := 0
	var superseded []string
	var expired []workspaceRef
	now := time.Now().UTC()
	for _, existing := range refs {
		switch {
		case strings.HasSuffix(existing.name, "/"+sha):
			superseded = append(superseded, existing.name)
		case workspaceRefExpired(existing.name, workspaceRefPrefix, now, maxAge):
			expired = append(expired, existing)
		default:
			kept++
		}
	}
	if kept >= limit {
		_, _ = gitCmd("-C", bareRepo, "update-ref", "-d", seedRef)
		return fmt.Errorf("workspace ref limit %d reached", limit)
	}
	ref := fmt.Sprintf(workspaceRefPrefix+"%020d/%s", now.UnixNano(), sha)
	if out, err := gitCmd("-C", bareRepo, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("create workspace ref: %w: %s", err, out)
	}
	// safety: a retry of an older run still fetches its snapshot, so expiry archives the ref instead of dropping objects.
	for _, aged := range expired {
		archived := workspaceArchiveRefPrefix + strings.TrimPrefix(aged.name, workspaceRefPrefix)
		if out, err := gitCmd("-C", bareRepo, "update-ref", archived, aged.object); err != nil {
			return fmt.Errorf("archive workspace ref %s: %w: %s", aged.name, err, out)
		}
		superseded = append(superseded, aged.name)
	}
	for _, stale := range superseded {
		if deleted, deleteErr := gitCmd("-C", bareRepo, "update-ref", "-d", stale); deleteErr != nil {
			return fmt.Errorf("expire workspace ref %s: %w: %s", stale, deleteErr, deleted)
		}
	}
	if err := pruneWorkspaceArchive(bareRepo, limit, maxAge, now); err != nil {
		return err
	}
	if out, err := gitCmd("-C", bareRepo, "update-ref", "-d", seedRef); err != nil {
		return fmt.Errorf("remove transient seed ref: %w: %s", err, out)
	}
	return nil
}

func pruneWorkspaceArchive(bareRepo string, limit int, maxAge time.Duration, now time.Time) error {
	archived, err := listWorkspaceRefs(bareRepo, workspaceArchiveRefPrefix)
	if err != nil {
		return err
	}
	for i, aged := range archived {
		if i < limit && !workspaceRefExpired(aged.name, workspaceArchiveRefPrefix, now, maxAge*workspaceArchiveAgeFactor) {
			continue
		}
		if out, err := gitCmd("-C", bareRepo, "update-ref", "-d", aged.name); err != nil {
			return fmt.Errorf("expire archived workspace ref %s: %w: %s", aged.name, err, out)
		}
	}
	return nil
}

func workspaceRefExpired(ref, prefix string, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	stamp, _, ok := strings.Cut(strings.TrimPrefix(ref, prefix), "/")
	if !ok {
		return false
	}
	nanos, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(0, nanos)) > maxAge
}

func pruneUnreachableSeedObjects(bareRepo string) {
	if out, err := gitCmd("-C", bareRepo, "reflog", "expire", "--expire=now", "--all"); err != nil {
		log.Printf("warning: seed reflog prune failed: %v %s", err, out)
	}
	if out, err := gitCmd("-C", bareRepo, "gc", "--prune=now"); err != nil {
		log.Printf("warning: seed object prune failed: %v %s", err, out)
	}
}

var (
	repoNames   = map[string]string{}
	repoNamesMu sync.RWMutex
	namesFile   = "/data/repo-names.json"
)

func loadRepoNames() {
	data, err := os.ReadFile(namesFile)
	if err != nil {
		return
	}
	stored := map[string]string{}
	if err := json.Unmarshal(data, &stored); err != nil {
		log.Printf("warning: failed to parse %s: %v", namesFile, err)
		return
	}
	// safety: the table is replayed into git clone, so an entry written before validation is refused here.
	for name, repoURL := range stored {
		validated, err := sourceurl.ValidateCloneURL(repoURL)
		if err != nil {
			// #nosec G706 -- the repo name is logged quoted and the validator quotes any URL text it echoes
			log.Printf("warning: dropping repo %q from %s: %v", name, namesFile, err)
			continue
		}
		repoNames[name] = validated
	}
	if len(repoNames) > 0 {
		log.Printf("loaded %d repo name mappings", len(repoNames))
	}
}

func saveRepoNames() {
	data, _ := json.MarshalIndent(repoNames, "", "  ")
	if err := os.WriteFile(namesFile, data, 0o644); err != nil {
		log.Printf("warning: failed to write %s: %v", namesFile, err)
	}
}

var validRepoName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func handleGitRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	name := r.URL.Query().Get("name")
	repoURL := r.URL.Query().Get("repo")
	if name == "" || repoURL == "" {
		http.Error(w, "name and repo required", http.StatusBadRequest)
		return
	}
	if !validRepoName.MatchString(name) {
		http.Error(w, "invalid name: must be 1-64 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}
	var err error
	repoURL, err = sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		http.Error(w, "invalid repo URL: "+err.Error(), http.StatusBadRequest)
		return
	}

	hash := repoHash(repoURL)

	repoNamesMu.Lock()
	existing, known := repoNames[name]
	repointing := known && existing != repoURL
	if repointing && !mayRepoint(r) {
		repoNamesMu.Unlock()
		http.Error(w, "name is already registered to another repository", http.StatusConflict)
		return
	}
	if repointing {
		// #nosec G706 -- the repository name is pattern-validated and the URL is redacted
		log.Printf("git register: repointing %q from %s to %s", name,
			sourceurl.Redact(existing), sourceurl.Redact(repoURL))
	}
	repoNames[name] = repoURL
	saveRepoNames()
	repoNamesMu.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		lock := repoLock(hash)
		lock.Lock()
		defer lock.Unlock()

		// #nosec G706 -- the repository name is pattern-validated and the URL is redacted
		log.Printf("git register: cloning %s as %q", sourceurl.Redact(repoURL), name)
		if out, err := gitCmd("clone", "--bare", "--", repoURL, bareRepo); err != nil {
			log.Printf("git register: clone failed (will need seed): %s %s", err, sshHint(out))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "hash": hash, "cloned": false})
			return
		}
		enableSHAFetch(bareRepo)
	} else {
		enableSHAFetch(bareRepo)
	}

	// #nosec G706 -- the repository name is pattern-validated and the URL is redacted
	log.Printf("git register: %s → %s (%s)", name, sourceurl.Redact(repoURL), hash)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "hash": hash, "cloned": true})
}

func handleGitRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	repoURL := r.URL.Query().Get("repo")
	if name == "" && repoURL == "" {
		http.Error(w, "name or repo query param required", http.StatusBadRequest)
		return
	}
	if repoURL == "" {
		repoNamesMu.RLock()
		repoURL = repoNames[name]
		repoNamesMu.RUnlock()
		if repoURL == "" {
			http.Error(w, fmt.Sprintf("repo %q not registered", name), http.StatusNotFound)
			return
		}
	}

	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		http.Error(w, fmt.Sprintf("repo not cached: %s", hash), http.StatusNotFound)
		return
	}

	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	enableSHAFetch(bareRepo)

	out, err := mirrorFetch(45*time.Second, bareRepo)
	if err != nil {
		log.Printf("eager refresh: %s failed: %v %s", hash, err, out)
		http.Error(w, fmt.Sprintf("fetch failed: %s\n%s", err, sshHint(out)), http.StatusBadGateway)
		return
	}
	bgFetch.markFetched(stateKey(hash))
	log.Printf("eager refresh: %s ok", hash)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "hash": hash})
}

func autoRegisterRepos() {
	if autoRegisterReposSpec == "" {
		return
	}
	for _, entry := range strings.Split(autoRegisterReposSpec, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			log.Printf("auto-register: skipping invalid entry %q (expected name=url)", entry)
			continue
		}
		name, repoURL := parts[0], parts[1]
		if !validRepoName.MatchString(name) {
			log.Printf("auto-register: skipping invalid name %q (want 1-64 alphanumeric/dash/underscore/dot chars)", name)
			continue
		}
		var err error
		repoURL, err = sourceurl.ValidateCloneURL(repoURL)
		if err != nil {
			log.Printf("auto-register: skipping invalid repo URL for %s: %v", name, err)
			continue
		}
		hash := repoHash(repoURL)

		repoNamesMu.Lock()
		repoNames[name] = repoURL
		saveRepoNames()
		repoNamesMu.Unlock()

		bareRepo := filepath.Join(repoDir, hash+".git")
		if _, err := os.Stat(bareRepo); err == nil {
			log.Printf("auto-register: %s already exists (%s)", name, hash[:8])
			enableSHAFetch(bareRepo)
			continue
		}
		lock := repoLock(hash)
		lock.Lock()
		log.Printf("auto-register: cloning %s (%s)", name, sourceurl.Redact(repoURL))
		if out, err := gitCmd("clone", "--bare", "--", repoURL, bareRepo); err != nil {
			log.Printf("auto-register: clone failed for %s: %v %s", name, err, sshHint(out))
		} else {
			enableSHAFetch(bareRepo)
			log.Printf("auto-register: %s ready", name)
		}
		lock.Unlock()
	}
}

func handleGit(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/git/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "usage: /git/<name>/info/refs or /git/<name>/git-upload-pack", http.StatusBadRequest)
		return
	}

	name := parts[0]
	rest := parts[1]

	bareRepo, err := resolveGitRepo(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch {
	case rest == "info/refs":
		service := r.URL.Query().Get("service")
		if service != "git-upload-pack" && service != "git-receive-pack" {
			http.Error(w, "unsupported service", http.StatusBadRequest)
			return
		}
		handleInfoRefs(w, r, bareRepo, service)

	case rest == "git-upload-pack":
		handleGitUploadPack(w, r, bareRepo)

	case rest == "git-receive-pack":
		handleGitReceivePack(w, r, bareRepo, name)

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func resolveGitRepo(name string) (string, error) {
	repoNamesMu.RLock()
	repoURL, ok := repoNames[name]
	repoNamesMu.RUnlock()

	if !ok {
		return "", fmt.Errorf("repo %q not registered -- POST /git/register?name=%s&repo=<url>", name, name)
	}

	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); err == nil {
		return bareRepo, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", bareRepo, err)
	}

	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()
	if _, err := os.Stat(bareRepo); err == nil {
		return bareRepo, nil
	}
	// #nosec G706 -- the repository name is pattern-validated and its URL was validated at registration
	log.Printf("gitcache: registered repo %q missing on disk; auto-cloning %s", name, repoURL)
	if out, err := gitCmd("clone", "--bare", "--", repoURL, bareRepo); err != nil {
		return "", fmt.Errorf(
			"repo %q registered but not cloned -- auto-clone failed (%w%s); seed manually via POST /sync/seed?repo=%s&sha=<commit>",
			name, err, sshHint(out), repoURL,
		)
	}
	enableSHAFetch(bareRepo)
	// #nosec G706 -- the repository name is pattern-validated
	log.Printf("gitcache: auto-clone complete for %q at %s", name, bareRepo)
	return bareRepo, nil
}

func handleInfoRefs(w http.ResponseWriter, r *http.Request, bareRepo, service string) {
	gitCmd := strings.TrimPrefix(service, "git-")
	// #nosec G702 -- the git subcommand is one of two literals, run as argv without a shell
	cmd := exec.Command("git", gitCmd, "--stateless-rpc", "--advertise-refs", bareRepo)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: git info/refs error for %s: %v: %s", bareRepo, err, stderr.String())
		http.Error(w, fmt.Sprintf("git error: %s", stderr.String()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	w.Header().Set("Cache-Control", "no-cache")

	header := fmt.Sprintf("# service=%s\n", service)
	// #nosec G705 -- the response is a git protocol advertisement, never HTML
	fmt.Fprintf(w, "%04x%s0000", len(header)+4, header)
	w.Write([]byte(stdout.String()))
}

func handleGitUploadPack(w http.ResponseWriter, r *http.Request, bareRepo string) {
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")

	cmd := exec.Command("git", "upload-pack", "--stateless-rpc", bareRepo)
	cmd.Stdin = r.Body
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("warning: git upload-pack error: %v", err)
	}
}

func handleGitReceivePack(w http.ResponseWriter, _ *http.Request, _, repoName string) {
	// #nosec G706 -- the repository name is pattern-validated
	log.Printf("git receive-pack rejected for %s -- gitcache is read-only", repoName)
	http.Error(w, "gitcache is read-only -- push directly to GitHub", http.StatusForbidden)
}

func cleanOldArchives(repoHash string) {
	entries, _ := os.ReadDir(archDir)
	type archiveEntry struct {
		name    string
		modTime time.Time
	}
	var matching []archiveEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), repoHash+"-") {
			info, _ := e.Info()
			matching = append(matching, archiveEntry{e.Name(), info.ModTime()})
		}
	}
	if len(matching) <= 5 {
		return
	}
	for i := 0; i < len(matching)-5; i++ {
		oldest := 0
		for j := range matching {
			if matching[j].modTime.Before(matching[oldest].modTime) {
				oldest = j
			}
		}
		_ = os.Remove(filepath.Join(archDir, matching[oldest].name))
		matching = append(matching[:oldest], matching[oldest+1:]...)
	}
}
