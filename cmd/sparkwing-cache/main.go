package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/logutil"
	"github.com/sparkwing-dev/sparkwing/otelutil"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	gitcacheArchiveServed metric.Int64Counter
	gitcacheFileServed    metric.Int64Counter
	gitcacheFetchDur      metric.Float64Histogram
	gitcacheCacheHits     metric.Int64Counter
	gitcacheCacheMisses   metric.Int64Counter
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
}

// Proxy metrics
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

// validGitRef matches safe git branch/tag names — no shell metacharacters.
var validGitRef = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

func validateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty git ref")
	}
	if !validGitRef.MatchString(ref) {
		return fmt.Errorf("invalid git ref %q: contains unsafe characters", ref)
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("invalid git ref %q: contains '..'", ref)
	}
	return nil
}

var (
	dataRoot     = "/data"
	repoDir      = "/data/repos"
	archDir      = "/data/archives"
	artifactsDir = "/data/artifacts"
	binsDir      = "/data/bins"
	cacheDir     = "/data/cache"
	// Per-repo locks to allow concurrent fetches of different repos
	repoLocks   = map[string]*sync.Mutex{}
	repoLocksMu sync.Mutex
)

func initDataDirs() {
	if d := os.Getenv("DATA_DIR"); d != "" {
		dataRoot = d
	}
	repoDir = filepath.Join(dataRoot, "repos")
	archDir = filepath.Join(dataRoot, "archives")
	artifactsDir = filepath.Join(dataRoot, "artifacts")
	binsDir = filepath.Join(dataRoot, "bins")
	cacheDir = filepath.Join(dataRoot, "cache")
	uploadsDir = filepath.Join(dataRoot, "uploads")
	namesFile = filepath.Join(dataRoot, "repo-names.json")
}

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

func main() {
	logutil.Init()
	initDataDirs()
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(archDir, 0o755)
	os.MkdirAll(artifactsDir, 0o755)
	os.MkdirAll(binsDir, 0o755)
	os.MkdirAll(cacheDir, 0o755)
	os.MkdirAll(uploadsDir, 0o755)
	loadRepoNames()

	// Initialize proxy (package registry cache)
	initProxy()

	tel := otelutil.Init(context.Background(), otelutil.Config{
		ServiceName: "sparkwing-cache",
	})
	initGitcacheMetrics()
	initProxyMetrics()

	// Set up SSH
	setupSSH()

	// Auto-register repos from env var
	autoRegisterRepos()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealthCombined)

	// Gitcache routes
	mux.HandleFunc("/archive", handleArchive)
	mux.HandleFunc("/repos", handleRepos)
	mux.HandleFunc("/artifacts/", handleArtifacts)
	mux.HandleFunc("/file", handleFile)
	mux.HandleFunc("/tree-hash", handleTreeHash)
	mux.HandleFunc("/branch-contains", handleBranchContains)
	mux.HandleFunc("/bin/", requireToken(handleBin))
	mux.HandleFunc("/cache/", requireToken(handleCache))
	mux.HandleFunc("/upload", requireToken(handleUpload))
	mux.HandleFunc("/uploads/", requireToken(handleUploadDownload))
	mux.HandleFunc("/sync/negotiate", requireToken(handleSyncNegotiate))
	mux.HandleFunc("/sync/seed", requireToken(handleSyncSeed))
	mux.HandleFunc("/git/register", handleGitRegister)
	mux.HandleFunc("/git/refresh", handleGitRefresh)
	mux.HandleFunc("/git/", handleGit)

	// Proxy routes (package registry cache)
	mux.HandleFunc("/proxy/", handleProxy)
	mux.HandleFunc("/stats", handleProxyStats)

	mux.Handle("/metrics", tel.PromHandler)

	// Background loops
	go backgroundFetchLoop()
	go proxyCleanupLoop()

	handler := otelhttp.NewHandler(mux, "sparkwing-cache")

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // archives can be large
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		log.Printf("received %v - shutting down gracefully (30s timeout)", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tel.Shutdown(ctx)
		srv.Shutdown(ctx)
	}()

	log.Printf("sparkwing-cache listening on :%s (proxy cache: %s)", port, proxyDir)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Printf("cache stopped")
}

func setupSSH() {
	sshKeyDir := "/etc/ssh-key"
	if _, err := os.Stat(sshKeyDir); err != nil {
		log.Printf("warning: no SSH key at %s — only public repos will work", sshKeyDir)
		return
	}

	home, _ := os.UserHomeDir()
	sshDir := filepath.Join(home, ".ssh")
	os.MkdirAll(sshDir, 0o700)

	// Copy keys — k8s secret mounts strip trailing newlines, so ensure
	// private keys end with one (OpenSSH requires it).
	entries, _ := os.ReadDir(sshKeyDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(sshKeyDir, e.Name()))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		os.WriteFile(filepath.Join(sshDir, e.Name()), data, 0o600)
	}

	os.Setenv("GIT_SSH_COMMAND", "ssh -i "+filepath.Join(sshDir, "id_ed25519")+" -o UserKnownHostsFile="+filepath.Join(sshDir, "known_hosts")+" -o StrictHostKeyChecking=yes")
	log.Printf("SSH key configured from %s", sshKeyDir)
}

// requireToken bears auth on external requests; in-cluster callers
// (no X-Forwarded-For) skip. Empty SPARKWING_API_TOKEN disables auth.
func requireToken(next http.HandlerFunc) http.HandlerFunc {
	token := os.Getenv("SPARKWING_API_TOKEN")
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		got := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			next(w, r)
			return
		}
		// In-cluster callers (controller, runner) don't set auth headers.
		// Distinguish by checking for the X-Forwarded-For header that the
		// ingress controller sets on external traffic.
		if r.Header.Get("X-Forwarded-For") == "" {
			next(w, r)
			return
		}
		http.Error(w, "unauthorized — set Authorization: Bearer <token> header", http.StatusUnauthorized)
	}
}

// fetchState is shared between the background fetch loop and /health.
type fetchState struct {
	mu         sync.RWMutex
	repos      map[string]*repoFetchState // keyed by bare repo dirname (e.g. "abc123.git")
	allFailing bool                       // true if last cycle had 100% failure
}

type repoFetchState struct {
	lastError   string    // empty on success
	lastErrorAt time.Time // when the last error occurred
	nextRetry   time.Time // backoff: when to retry
	backoff     time.Duration
}

var bgFetch = &fetchState{repos: map[string]*repoFetchState{}}

func (fs *fetchState) problems() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	var msgs []string
	if fs.allFailing {
		msgs = append(msgs, "All git fetches are failing — SSH may be broken or the pod is resource-exhausted")
	}
	for name, rs := range fs.repos {
		if rs.lastError == "" {
			continue
		}
		// Only report errors from the last 10 minutes (stale errors aren't interesting)
		if time.Since(rs.lastErrorAt) > 10*time.Minute {
			continue
		}
		msg := fmt.Sprintf("repo %s: %s", strings.TrimSuffix(name, ".git"), friendlyFetchError(rs.lastError))
		msgs = append(msgs, msg)
	}
	return msgs
}

// friendlyFetchError translates raw git/SSH errors into actionable messages.
func friendlyFetchError(raw string) string {
	switch {
	case strings.Contains(raw, "cannot fork"):
		return "cannot fork SSH process — pod is out of PIDs or memory"
	case strings.Contains(raw, "Permission denied"):
		return "SSH permission denied — check that the SSH key has read access to this repo"
	case strings.Contains(raw, "Host key verification failed"):
		return "SSH host key verification failed — known_hosts may be missing or stale"
	case strings.Contains(raw, "Could not resolve hostname"):
		return "DNS resolution failed — check network connectivity"
	case strings.Contains(raw, "Connection refused"):
		return "SSH connection refused — GitHub may be unreachable from this cluster"
	case strings.Contains(raw, "timed out"):
		return "git fetch timed out — slow network or large repo"
	default:
		// Truncate raw error to something reasonable
		if len(raw) > 120 {
			return raw[:120] + "..."
		}
		return raw
	}
}

// backgroundFetchLoop keeps gitcache fresh. Per-repo failure doubles
// the retry interval (cap 10m); a 100% failure cycle backs the whole
// loop off to avoid fork-exhaustion death spirals.
func backgroundFetchLoop() {
	interval := 30 * time.Second
	if s := os.Getenv("FETCH_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			interval = d
		}
	}
	log.Printf("background fetch: every %s", interval)

	const maxBackoff = 10 * time.Minute
	consecutiveAllFail := 0

	for {
		time.Sleep(interval)

		entries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}

		var fetched, failed int
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
				continue
			}

			// Check per-repo backoff
			bgFetch.mu.RLock()
			rs := bgFetch.repos[e.Name()]
			bgFetch.mu.RUnlock()
			if rs != nil && time.Now().Before(rs.nextRetry) {
				continue // still in backoff
			}

			bare := filepath.Join(repoDir, e.Name())
			mu := repoLock(bare)
			mu.Lock()
			fetchStart := time.Now()
			out, err := gitCmdTimeout(1*time.Minute, "-C", bare, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")
			mu.Unlock()
			if gitcacheFetchDur != nil {
				gitcacheFetchDur.Record(context.Background(), time.Since(fetchStart).Seconds(),
					metric.WithAttributes(attribute.String("repo", e.Name())))
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
				// Success: clear error state
				if rs != nil {
					rs.lastError = ""
					rs.backoff = 0
				}
				bgFetch.mu.Unlock()
				log.Printf("background fetch: %s ok", e.Name())
			}
		}

		// Global circuit breaker: if every attempted fetch failed, back off
		// the entire loop to avoid hammering a broken SSH/resource state.
		if fetched > 0 && failed == fetched {
			consecutiveAllFail++
			bgFetch.mu.Lock()
			bgFetch.allFailing = true
			bgFetch.mu.Unlock()
			pause := min(time.Duration(consecutiveAllFail)*interval, maxBackoff)
			log.Printf("background fetch: all %d repos failed — pausing %s", failed, pause)
			time.Sleep(pause)
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
		os.Remove(testPath)
	}

	if len(problems) > 0 {
		resp["status"] = "degraded"
		resp["problems"] = problems
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GET /archive?repo=...&branch=... → tar.gz, cached by commit hash.
func handleArchive(w http.ResponseWriter, r *http.Request) {
	repoURL := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")
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

	// Clone or fetch (with corrupt repo recovery)
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		log.Printf("background fetch: cloning %s → %s", repoURL, hash)
		if out, err := gitCmd("clone", "--bare", repoURL, bareRepo); err != nil {
			http.Error(w, fmt.Sprintf("clone failed: %s\n%s", err, sshHint(out)), http.StatusInternalServerError)
			return
		}
		enableSHAFetch(bareRepo)
	} else {
		enableSHAFetch(bareRepo)
		log.Printf("background fetch: fetching %s", hash)
		if out, err := gitCmd("-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
			// Fetch failed — repo may be corrupt from a previous partial clone/crash.
			// Remove and reclone rather than leaving a permanently broken repo.
			log.Printf("warning: fetch failed for %s, attempting recovery reclone: %v", hash, err)
			os.RemoveAll(bareRepo)
			if recloneOut, err2 := gitCmd("clone", "--bare", repoURL, bareRepo); err2 != nil {
				http.Error(w, fmt.Sprintf("fetch failed: %s\n%s\nreclone also failed: %s %s", err, sshHint(out), err2, recloneOut), http.StatusInternalServerError)
				return
			}
			enableSHAFetch(bareRepo)
			log.Printf("recovery reclone succeeded for %s", hash)
		}
	}

	// Resolve branch to commit
	commitBytes, err := exec.Command("git", "-C", bareRepo, "rev-parse", branch).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("branch %q not found", branch), http.StatusNotFound)
		return
	}
	commit := strings.TrimSpace(string(commitBytes))
	if len(commit) < 8 {
		http.Error(w, fmt.Sprintf("unexpected commit hash for branch %q: %q", branch, commit), http.StatusInternalServerError)
		return
	}
	shortCommit := commit[:8]

	// Check tarball cache
	tarball := filepath.Join(archDir, hash+"-"+shortCommit+".tar.gz")
	if _, err := os.Stat(tarball); err == nil {
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

	// Generate tarball — use piped commands instead of sh -c to avoid injection
	log.Printf("cache hit: archiving %s@%s", hash, shortCommit)
	tmpTar := tarball + ".tmp"
	if err := archiveToFile(bareRepo, branch, tmpTar); err != nil {
		os.Remove(tmpTar)
		http.Error(w, fmt.Sprintf("archive failed: %s", err), http.StatusInternalServerError)
		return
	}
	os.Rename(tmpTar, tarball)

	// Clean old tarballs for this repo (keep last 5)
	cleanOldArchives(hash)

	serveTarball(w, r, tarball, hash, commit)
}

func serveTarball(w http.ResponseWriter, r *http.Request, path, hash, commit string) {
	w.Header().Set("X-Commit", commit)
	w.Header().Set("X-Repo-Hash", hash)
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
	json.NewEncoder(w).Encode(repos)
}

// sshHint returns a helpful message if the git error looks like an SSH auth failure.
func sshHint(output string) string {
	if strings.Contains(output, "Permission denied") || strings.Contains(output, "Host key verification failed") {
		return "hint: SSH key rejected — run: sparkwing cluster update-ssh-key --name <cluster> --github-ssh-key ~/.ssh/<your-key>"
	}
	return ""
}

// gitCmd runs a git command with a default 2-minute timeout.
// Prevents hung git operations (network issues, unresponsive remotes)
// from blocking HTTP handlers indefinitely.
func gitCmd(args ...string) (string, error) {
	return gitCmdTimeout(2*time.Minute, args...)
}

// enableSHAFetch flips uploadpack.allowReachableSHA1InWant so runners
// can `git fetch --depth 1 origin <SHA>`. Idempotent; failure logs only.
func enableSHAFetch(bareRepo string) {
	if out, err := gitCmd("-C", bareRepo, "config",
		"uploadpack.allowReachableSHA1InWant", "true"); err != nil {
		log.Printf("warning: enableSHAFetch on %s failed: %v %s", bareRepo, err, out)
	}
}

// gitForkSem caps concurrent git subprocesses; webhook bursts at
// 512-1024Mi limits otherwise hit fork() EAGAIN. Override with
// SPARKWING_GITCACHE_CONCURRENCY.
var gitForkSem = make(chan struct{}, gitForkLimit())

func gitForkLimit() int {
	if v := os.Getenv("SPARKWING_GITCACHE_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

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

	cmd := exec.CommandContext(ctx, "git", args...)
	// Process group + group-kill: a plain timeout would orphan SSH children.
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

// archiveToFile runs `git archive --format=tar <branch> | gzip > <outPath>`
// using piped exec commands — no shell involved, safe from injection.
func archiveToFile(bareRepo, branch, outPath string) error {
	gitArchive := exec.Command("git", "-C", bareRepo, "archive", "--format=tar", "--", branch)
	gzipCmd := exec.Command("gzip")

	// Pipe git archive stdout into gzip stdin
	pipe, err := gitArchive.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe setup: %w", err)
	}
	gzipCmd.Stdin = pipe

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
		gitArchive.Process.Kill()
		return fmt.Errorf("gzip start: %w", err)
	}

	if err := gitArchive.Wait(); err != nil {
		gzipCmd.Process.Kill()
		return fmt.Errorf("git archive: %w", err)
	}
	if err := gzipCmd.Wait(); err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	return nil
}

// GET /file?repo=X&branch=Y&path=.sparkwing/pipelines.yaml
// Returns the raw content of a single file from the cached bare repo.
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
		http.Error(w, "repo not cached — trigger an archive first", http.StatusNotFound)
		return
	}

	// Fetch latest
	gitCmd("-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")

	// git show branch:path
	out, err := exec.Command("git", "-C", bareRepo, "show", branch+":"+filePath).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("file not found: %s:%s", branch, filePath), http.StatusNotFound)
		return
	}

	if gitcacheFileServed != nil {
		gitcacheFileServed.Add(r.Context(), 1)
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(out)
}

// GET /tree-hash?repo=X&branch=Y&path=services/api
// Returns the git tree hash for a subdirectory — content-addressable.
// Same content = same hash, regardless of commit.
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

	// Fetch latest
	gitCmd("-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")

	// Get tree hash: git rev-parse branch:path (or branch for root)
	ref := branch
	if path != "" {
		ref = branch + ":" + path
	}

	out, err := exec.Command("git", "-C", bareRepo, "rev-parse", ref).Output()
	if err != nil {
		http.Error(w, fmt.Sprintf("path not found: %s", ref), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(strings.TrimSpace(string(out))))
}

// GET /branch-contains?repo=X&branch=main&commit=abc123
// Returns 200 if the commit is an ancestor of the branch, 404 otherwise.
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

	// Fetch latest
	gitCmd("-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")

	// Check if commit is an ancestor of branch
	err := exec.Command("git", "-C", bareRepo, "merge-base", "--is-ancestor", commit, branch).Run()
	if err != nil {
		http.Error(w, fmt.Sprintf("commit %s is not on branch %s", commit, branch), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "commit %s is on branch %s", commit, branch)
}

// --- Artifacts ---

// POST /artifacts/{jobID}?path=coverage/report.html — upload a file
// GET  /artifacts/{jobID}?glob=*.html — download artifacts as tar.gz
// GET  /artifacts/{jobID} — list artifacts for a job
// handleBin serves compiled pipeline binaries by content hash.
var validBinHash = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{8}){0,3}$`)

func handleBin(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/bin/")
	if !validBinHash.MatchString(hash) {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}

	path := filepath.Join(binsDir, hash)

	switch r.Method {
	case http.MethodGet:
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		io.Copy(w, f)

	case http.MethodPut:
		// Limit to 100MB
		r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		if err := os.WriteFile(path, data, 0o755); err != nil {
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		log.Printf("bin cache: stored %s (%d bytes)", hash, len(data))
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "GET or PUT only", http.StatusMethodNotAllowed)
	}
}

// Cache key validation: alphanumeric, hyphens, underscores, dots, 1-128 chars.
// Keys are user-generated from lockfile hashes, e.g. "gems-abc123def456".
var validCacheKey = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// handleCache implements a content-addressed blob store for dependency caches.
// Pipelines tar up their dependency directories (gems, node_modules, etc.)
// and store/restore them keyed by a hash of the lockfile.
func handleCache(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/cache/")
	if !validCacheKey.MatchString(key) {
		http.Error(w, "invalid cache key: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}

	path := filepath.Join(cacheDir, key+".tar.gz")

	switch r.Method {
	case http.MethodHead:
		if _, err := os.Stat(path); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		f, err := os.Open(path)
		if err != nil {
			if gitcacheCacheMisses != nil {
				gitcacheCacheMisses.Add(r.Context(), 1, metric.WithAttributes(
					attribute.String("type", "dependency")))
			}
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		if gitcacheCacheHits != nil {
			gitcacheCacheHits.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("type", "dependency")))
		}
		log.Printf("cache hit: %s (%d bytes)", key, info.Size())
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		io.Copy(w, f)

	case http.MethodPut:
		// 500MB max — dependency caches can be large (node_modules, etc.)
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
			os.Remove(tmpPath)
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		if err := os.Rename(tmpPath, path); err != nil {
			os.Remove(tmpPath)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		log.Printf("cache store: %s (%d bytes)", key, n)
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "GET, HEAD, or PUT only", http.StatusMethodNotAllowed)
	}
}

func handleArtifacts(w http.ResponseWriter, r *http.Request) {
	// Parse /artifacts/{jobID}[/path...]
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "job ID required: /artifacts/{jobID}", http.StatusBadRequest)
		return
	}
	jobID := parts[0]

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

	// Sanitize path to prevent directory traversal
	artifactPath = filepath.Clean(artifactPath)
	if strings.Contains(artifactPath, "..") || filepath.IsAbs(artifactPath) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	// Verify resolved path stays within the job's artifact directory
	jobDir := filepath.Join(artifactsDir, jobID)
	dest := filepath.Join(jobDir, artifactPath)
	absJobDir, _ := filepath.Abs(jobDir)
	absDest, _ := filepath.Abs(dest)
	if !strings.HasPrefix(absDest, absJobDir+string(filepath.Separator)) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	destDir := filepath.Dir(dest)
	os.MkdirAll(destDir, 0o755)
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

	log.Printf("describe: artifact uploaded %s/%s (%d bytes)", jobID, artifactPath, n)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"path": artifactPath, "size": n})
}

func artifactDownload(w http.ResponseWriter, r *http.Request, jobID string) {
	glob := r.URL.Query().Get("glob")
	jobDir := filepath.Join(artifactsDir, jobID)

	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		http.Error(w, "no artifacts for job "+jobID, http.StatusNotFound)
		return
	}

	// Find matching files
	var matches []string
	filepath.Walk(jobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(jobDir, path)
		if matched, _ := filepath.Match(glob, filepath.Base(rel)); matched {
			matches = append(matches, rel)
		}
		// Also try matching against the full relative path
		if matched, _ := filepath.Match(glob, rel); matched && !contains(matches, rel) {
			matches = append(matches, rel)
		}
		return nil
	})

	if len(matches) == 0 {
		http.Error(w, fmt.Sprintf("no artifacts matching %q for job %s", glob, jobID), http.StatusNotFound)
		return
	}

	// If single file, serve directly
	if len(matches) == 1 {
		http.ServeFile(w, r, filepath.Join(jobDir, matches[0]))
		return
	}

	// Multiple files: tar them up
	w.Header().Set("Content-Type", "application/tar")
	cmd := exec.Command("tar", append([]string{"-cf", "-", "-C", jobDir}, matches...)...)
	cmd.Stdout = w
	cmd.Run()
}

func artifactList(w http.ResponseWriter, r *http.Request, jobID string) {
	jobDir := filepath.Join(artifactsDir, jobID)

	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]string{})
		return
	}

	var files []string
	filepath.Walk(jobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(jobDir, path)
		files = append(files, rel)
		return nil
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- Uploads (local code sync) ---

var uploadsDir = "/data/uploads"

func init() {
	os.MkdirAll(uploadsDir, 0o755)
}

// POST /upload — accepts a tarball, stores with content-addressed ID, returns the ID.
// Optional query params:
//   - repo: git repo URL (for incremental sync)
//   - base: commit hash to overlay the upload on top of (incremental sync)
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Read the tarball body
	data, err := io.ReadAll(io.LimitReader(r.Body, 500<<20)) // 500MB max
	if err != nil {
		http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	repoURL := r.URL.Query().Get("repo")
	base := r.URL.Query().Get("base")

	if base != "" && repoURL != "" {
		// Incremental upload: overlay on top of base commit
		id, size, err := handleIncrementalUpload(data, repoURL, base)
		if err != nil {
			log.Printf("warning: incremental upload failed, storing as-is: %v", err)
			// Fall through to store the raw upload
		} else {
			log.Printf("describe: upload %s (incremental from %s, %d bytes)", id, base[:8], size)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": id, "size": size})
			return
		}
	}

	// Regular upload: store as-is
	id := fmt.Sprintf("%x", sha256.Sum256(data))[:16]
	path := filepath.Join(uploadsDir, id+".tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("describe: upload %s (%d bytes)", id, len(data))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": id, "size": len(data)})
}

// handleIncrementalUpload checks out the base commit, extracts the diff tarball on top,
// then creates a new combined tarball.
func handleIncrementalUpload(diffData []byte, repoURL, base string) (string, int, error) {
	if err := validateGitRef(base); err != nil {
		return "", 0, fmt.Errorf("invalid base ref: %w", err)
	}

	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		return "", 0, fmt.Errorf("repo not cached: %s", hash)
	}

	// Create temp work dir
	workDir, err := os.MkdirTemp("", "wing-incremental-*")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(workDir)

	// Checkout base commit
	if err := archiveToDir(bareRepo, base, workDir); err != nil {
		return "", 0, fmt.Errorf("checkout base %s: %w", base[:8], err)
	}

	// Write diff tarball to temp file
	tmpDiff, err := os.CreateTemp("", "wing-diff-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmpDiff.Name())
	if _, err := tmpDiff.Write(diffData); err != nil {
		tmpDiff.Close()
		return "", 0, fmt.Errorf("write diff: %w", err)
	}
	tmpDiff.Close()

	// Extract diff on top of base checkout (overwrites changed files)
	cmd := exec.Command("tar", "-xzf", tmpDiff.Name(), "-C", workDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("extract diff: %s: %w", string(out), err)
	}

	// Create combined tarball
	tmpCombined, err := os.CreateTemp("", "wing-combined-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmpCombined.Name())
	tmpCombined.Close()

	tarCmd := exec.Command("tar", "-czf", tmpCombined.Name(), "-C", workDir, ".")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("create combined tarball: %s: %w", string(out), err)
	}

	// Read and store
	combined, err := os.ReadFile(tmpCombined.Name())
	if err != nil {
		return "", 0, err
	}

	id := fmt.Sprintf("%x", sha256.Sum256(combined))[:16]
	path := filepath.Join(uploadsDir, id+".tar.gz")
	if err := os.WriteFile(path, combined, 0o644); err != nil {
		return "", 0, err
	}

	return id, len(combined), nil
}

// archiveToDir extracts a git archive of the given ref into a directory.
func archiveToDir(bareRepo, ref, dir string) error {
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
		gitArchive.Process.Kill()
		return err
	}
	if err := gitArchive.Wait(); err != nil {
		tarExtract.Process.Kill()
		return err
	}
	return tarExtract.Wait()
}

// GET /uploads/{id} — download a previously uploaded tarball.
func handleUploadDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/uploads/")
	id = strings.TrimSuffix(id, ".tar.gz")
	if id == "" {
		http.Error(w, "upload ID required", http.StatusBadRequest)
		return
	}

	path := filepath.Join(uploadsDir, id+".tar.gz")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		http.Error(w, "upload not found: "+id, http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, path)
}

// --- Sync negotiation ---

// POST /sync/negotiate — find common ancestor between wing's local commits and gitcache's repo.
// Request: {"repo": "git@...", "commits": ["abc123", "def456", ...]}
// Response: {"ancestor": "def456", "found": true} or {"ancestor": "", "found": false}
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

	hash := repoHash(req.Repo)
	bareRepo := filepath.Join(repoDir, hash+".git")

	// Check if we have this repo cached
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		// No cached repo — can't negotiate, wing should send full tarball
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ancestor": "", "found": false})
		return
	}

	// Fetch latest to make sure we're up to date
	lock := repoLock(hash)
	lock.Lock()
	gitCmd("-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")
	lock.Unlock()

	// Walk the client's commit list and find the first one we have
	for _, commit := range req.Commits {
		// Check if this commit exists in our repo
		err := exec.Command("git", "-C", bareRepo, "cat-file", "-t", commit).Run()
		if err == nil {
			log.Printf("sync negotiate: found common ancestor %s for %s", commit[:8], hash)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ancestor": commit, "found": true})
			return
		}
	}

	// No common ancestor found
	log.Printf("sync negotiate: no common ancestor for %s (%d commits checked)", hash, len(req.Commits))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ancestor": "", "found": false})
}

// POST /sync/seed?repo=git@github.com:user/repo.git — receive a git bundle and create/update a bare repo.
// This lets the gitcache have git history without needing SSH access to clone.
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

	// Read bundle data
	bundleData, err := io.ReadAll(io.LimitReader(r.Body, 500<<20)) // 500MB max
	if err != nil {
		http.Error(w, "read failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	hash := repoHash(repoURL)
	lock := repoLock(hash)
	lock.Lock()
	defer lock.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")

	// Write bundle to temp file
	tmpBundle, err := os.CreateTemp("", "seed-*.bundle")
	if err != nil {
		http.Error(w, "temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpBundle.Name())
	tmpBundle.Write(bundleData)
	tmpBundle.Close()

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		// Clone from the bundle to create the bare repo
		log.Printf("seed: creating bare repo from bundle for %s", hash)
		if out, err := gitCmd("clone", "--bare", tmpBundle.Name(), bareRepo); err != nil {
			http.Error(w, fmt.Sprintf("clone from bundle failed: %s\n%s", err, out), http.StatusInternalServerError)
			return
		}
		// Set the origin URL so future fetches (if SSH becomes available) work
		gitCmd("-C", bareRepo, "remote", "set-url", "origin", repoURL)
		enableSHAFetch(bareRepo)
	} else {
		enableSHAFetch(bareRepo)
		// Fetch from the bundle to update existing repo
		log.Printf("seed: updating bare repo from bundle for %s", hash)
		if out, err := gitCmd("-C", bareRepo, "fetch", tmpBundle.Name(), "+refs/*:refs/*"); err != nil {
			// Try individual refs
			log.Printf("seed: bulk fetch failed (%s), trying refs/heads/*", out)
			gitCmd("-C", bareRepo, "fetch", tmpBundle.Name(), "+refs/heads/*:refs/heads/*")
		}
	}

	log.Printf("seed: %s seeded (%d bytes)", hash, len(bundleData))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "size": len(bundleData)})
}

// --- Git Smart HTTP Backend ---
// Allows git clone/push over HTTP without SSH keys.

var (
	repoNames   = map[string]string{} // name → repoURL
	repoNamesMu sync.RWMutex
	namesFile   = "/data/repo-names.json"
)

func loadRepoNames() {
	data, err := os.ReadFile(namesFile)
	if err == nil {
		json.Unmarshal(data, &repoNames)
		if len(repoNames) > 0 {
			log.Printf("loaded %d repo name mappings", len(repoNames))
		}
	}
}

func saveRepoNames() {
	data, _ := json.MarshalIndent(repoNames, "", "  ")
	os.WriteFile(namesFile, data, 0o644)
}

// POST /git/register?name=gitops&repo=git@github.com:user/repo.git
// Registers a friendly name for a repo URL. If the repo isn't cached yet and
// SSH is available, clones it. Otherwise it can be seeded via /sync/seed.
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

	hash := repoHash(repoURL)

	repoNamesMu.Lock()
	repoNames[name] = repoURL
	saveRepoNames()
	repoNamesMu.Unlock()

	bareRepo := filepath.Join(repoDir, hash+".git")

	// If repo doesn't exist, try to clone it
	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		lock := repoLock(hash)
		lock.Lock()
		defer lock.Unlock()

		log.Printf("git register: cloning %s as %q", repoURL, name)
		if out, err := gitCmd("clone", "--bare", repoURL, bareRepo); err != nil {
			// Clone failed (probably no SSH key) — that's OK, it can be seeded later
			log.Printf("git register: clone failed (will need seed): %s %s", err, sshHint(out))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"name": name, "hash": hash, "cloned": false})
			return
		}
		enableSHAFetch(bareRepo)
	} else {
		// Existing bare repo: still poke the config so repos created
		// before get migrated on the next register call.
		enableSHAFetch(bareRepo)
	}

	log.Printf("git register: %s → %s (%s)", name, repoURL, hash)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"name": name, "hash": hash, "cloned": true})
}

// POST /git/refresh?name=<friendly-name>  (or ?repo=<url>)
//
// Synchronously runs `git fetch` on the named bare repo so a freshly
// pushed SHA shows up before the next dispatch tries to fetch it.
// Closes the gitcache-lag race window in IMP-005. Best-effort: callers
// pass a short timeout and continue on failure.
//
// Either `name` (preferred — already registered) or `repo` (full URL,
// auto-resolves via repoHash) works. Returns 404 if neither resolves
// to a cached bare repo. Concurrent refreshes coalesce on the per-repo
// lock so a webhook burst doesn't fan out N fetches.
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
	// Prefer name -> URL lookup so the bare-repo path matches register's hash.
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
	out, err := gitCmdTimeout(45*time.Second, "-C", bareRepo, "fetch", "--prune", "origin", "+refs/heads/*:refs/heads/*")
	if err != nil {
		log.Printf("eager refresh: %s failed: %v %s", hash, err, out)
		http.Error(w, fmt.Sprintf("fetch failed: %s\n%s", err, sshHint(out)), http.StatusBadGateway)
		return
	}
	log.Printf("eager refresh: %s ok", hash)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "hash": hash})
}

// autoRegisterRepos registers repos listed in GITCACHE_REPOS env var on
// startup. Format: comma-separated "name=url" pairs, e.g.
// "gitops=git@github.com:your-org/gitops.git,my-app=git@github.com:..."
func autoRegisterRepos() {
	repos := os.Getenv("GITCACHE_REPOS")
	if repos == "" {
		return
	}
	for _, entry := range strings.Split(repos, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			log.Printf("auto-register: skipping invalid entry %q (expected name=url)", entry)
			continue
		}
		name, repoURL := parts[0], parts[1]
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
		log.Printf("auto-register: cloning %s (%s)", name, repoURL)
		if out, err := gitCmd("clone", "--bare", repoURL, bareRepo); err != nil {
			log.Printf("auto-register: clone failed for %s: %v %s", name, err, sshHint(out))
		} else {
			enableSHAFetch(bareRepo)
			log.Printf("auto-register: %s ready", name)
		}
		lock.Unlock()
	}
}

// handleGit routes git smart HTTP protocol requests.
// URL pattern: /git/<name>/info/refs, /git/<name>/git-upload-pack, /git/<name>/git-receive-pack
func handleGit(w http.ResponseWriter, r *http.Request) {
	// Parse: /git/<name>/<rest>
	path := strings.TrimPrefix(r.URL.Path, "/git/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "usage: /git/<name>/info/refs or /git/<name>/git-upload-pack", http.StatusBadRequest)
		return
	}

	name := parts[0]
	rest := parts[1]

	// Resolve name to bare repo path
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
		return "", fmt.Errorf("repo %q not registered — POST /git/register?name=%s&repo=<url>", name, name)
	}

	hash := repoHash(repoURL)
	bareRepo := filepath.Join(repoDir, hash+".git")

	if _, err := os.Stat(bareRepo); os.IsNotExist(err) {
		return "", fmt.Errorf("repo %q registered but not cloned — seed via POST /sync/seed?repo=%s", name, repoURL)
	}

	return bareRepo, nil
}

func handleInfoRefs(w http.ResponseWriter, r *http.Request, bareRepo, service string) {
	// service is "git-upload-pack" or "git-receive-pack" — strip "git-" prefix for the command
	gitCmd := strings.TrimPrefix(service, "git-")
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

	// Write pkt-line service header
	header := fmt.Sprintf("# service=%s\n", service)
	fmt.Fprintf(w, "%04x%s0000", len(header)+4, header)
	// Write ref advertisement
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
	log.Printf("git receive-pack rejected for %s — gitcache is read-only", repoName)
	http.Error(w, "gitcache is read-only — push directly to GitHub", http.StatusForbidden)
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
	// Keep last 5
	if len(matching) <= 5 {
		return
	}
	// Sort by mod time, remove oldest
	for i := 0; i < len(matching)-5; i++ {
		oldest := 0
		for j := range matching {
			if matching[j].modTime.Before(matching[oldest].modTime) {
				oldest = j
			}
		}
		os.Remove(filepath.Join(archDir, matching[oldest].name))
		matching = append(matching[:oldest], matching[oldest+1:]...)
	}
}
