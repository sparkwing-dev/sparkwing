// Package cache is the sparkwing-cache service: an HTTP gitcache,
// artifact / bin / cache blob store, local-code upload sync, and a
// pass-through package-registry proxy. cmd/sparkwing-cache is a
// thin wrapper that parses flags and calls [Run].
//
// The package owns its filesystem layout: every directory is rooted
// at [Config.DataDir] (proxy state is independent under
// [Config.ProxyDir]). [New] resolves the layout from Config and
// creates the directories; the previous implementation relied on
// init() running before main(), which was fragile and only worked
// by coincidence with the default /data/* paths.
package cache

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/sparkwing-dev/sparkwing/internal/logutil"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

// Config holds every tunable knob the cache service accepts. All
// fields have non-zero defaults supplied by [DefaultConfig]; the
// flag-parsing layer in cmd/sparkwing-cache merges flag values +
// environment fallbacks on top.
type Config struct {
	// Addr is the bind address, e.g. ":8090".
	Addr string

	// DataDir roots the gitcache data layout (repos/, archives/,
	// artifacts/, bins/, cache/, uploads/, plus the repo-names.json
	// mapping file).
	DataDir string

	// ProxyDir roots the package-registry proxy cache.
	ProxyDir string

	// FetchInterval is the cadence of the background gitcache fetch
	// loop. Per-repo backoff doubles on failure (cap 10m).
	FetchInterval time.Duration

	// ProxyCacheTTL bounds how long mutable proxy responses are
	// served from cache before re-fetching upstream.
	ProxyCacheTTL time.Duration

	// ProxyMaxAge is the cleanup threshold for immutable proxy
	// entries (typically content-addressed files like .tgz).
	ProxyMaxAge time.Duration

	// APIToken gates write endpoints (/bin, /cache, /upload, /sync,
	// /uploads). Empty disables auth so in-cluster callers without
	// the header keep working. External callers must send
	// Authorization: Bearer <token>.
	APIToken string

	// AutoRegisterRepos is a comma-separated list of "name=url"
	// pairs that get cloned into the gitcache on startup. Empty
	// skips auto-registration.
	AutoRegisterRepos string

	// SSHKeyDir is the directory the SSH key is mounted at (a k8s
	// secret in production). The contents are copied into ~/.ssh
	// at startup with a trailing newline appended to satisfy
	// OpenSSH. Missing directory degrades to public-repo-only.
	SSHKeyDir string

	// GitForkLimit caps concurrent git subprocesses. Webhook bursts
	// at tight memory limits otherwise hit fork() EAGAIN.
	GitForkLimit int
}

// DefaultConfig returns the same defaults the service shipped with
// before the refactor.
func DefaultConfig() Config {
	return Config{
		Addr:          ":8090",
		DataDir:       "/data",
		ProxyDir:      "/data/proxy",
		FetchInterval: 30 * time.Second,
		ProxyCacheTTL: 10 * time.Minute,
		ProxyMaxAge:   7 * 24 * time.Hour,
		SSHKeyDir:     "/etc/ssh-key",
		GitForkLimit:  4,
	}
}

// Server owns the cache's HTTP mux + telemetry + background-loop
// wait group. Construct via [New]; drive via [Server.Run].
type Server struct {
	cfg  Config
	tel  *otelutil.Telemetry
	mux  *http.ServeMux
	http *http.Server
	wg   sync.WaitGroup
}

// New resolves a Config onto the package's filesystem layout,
// creates every required directory, loads persisted repo-name
// mappings, initialises metrics, sets up SSH, and auto-registers
// repos from Config.AutoRegisterRepos. Returns a ready-to-Run
// *Server.
//
// Every effect that the legacy init() / initDataDirs() chain used
// to perform at package-load time now happens here, AFTER the
// caller has supplied a validated Config -- so the order of
// operations is deterministic and reviewable in one place.
func New(cfg Config) (*Server, error) {
	logutil.Init()
	if cfg.Addr == "" {
		cfg.Addr = ":8090"
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("cache: DataDir is required")
	}
	if cfg.ProxyDir == "" {
		cfg.ProxyDir = filepath.Join(cfg.DataDir, "proxy")
	}
	if cfg.FetchInterval <= 0 {
		cfg.FetchInterval = 30 * time.Second
	}
	if cfg.ProxyCacheTTL <= 0 {
		cfg.ProxyCacheTTL = 10 * time.Minute
	}
	if cfg.ProxyMaxAge <= 0 {
		cfg.ProxyMaxAge = 7 * 24 * time.Hour
	}
	if cfg.SSHKeyDir == "" {
		cfg.SSHKeyDir = "/etc/ssh-key"
	}
	if cfg.GitForkLimit <= 0 {
		cfg.GitForkLimit = 4
	}

	// Apply Config to the package-level path / interval vars the
	// existing handlers + background loops still read directly.
	// (Converting every handler to a method on *Server is a future
	// cleanup; this preserves behavior with the smallest diff.)
	dataRoot = cfg.DataDir
	repoDir = filepath.Join(cfg.DataDir, "repos")
	archDir = filepath.Join(cfg.DataDir, "archives")
	artifactsDir = filepath.Join(cfg.DataDir, "artifacts")
	binsDir = filepath.Join(cfg.DataDir, "bins")
	cacheDir = filepath.Join(cfg.DataDir, "cache")
	uploadsDir = filepath.Join(cfg.DataDir, "uploads")
	namesFile = filepath.Join(cfg.DataDir, "repo-names.json")
	proxyDir = cfg.ProxyDir
	proxyCacheTTL = cfg.ProxyCacheTTL
	proxyMaxAge = cfg.ProxyMaxAge
	apiToken = cfg.APIToken
	sshKeyDir = cfg.SSHKeyDir
	autoRegisterReposSpec = cfg.AutoRegisterRepos
	gitForkSem = make(chan struct{}, cfg.GitForkLimit)

	for _, d := range []string{repoDir, archDir, artifactsDir, binsDir, cacheDir, uploadsDir, proxyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("cache: mkdir %s: %w", d, err)
		}
	}
	loadRepoNames()
	initProxy()
	initGitcacheMetrics()
	initProxyMetrics()
	setupSSH()
	autoRegisterRepos()

	s := &Server{cfg: cfg}
	s.tel = otelutil.Init(context.Background(), otelutil.Config{ServiceName: "sparkwing-cache"})

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/health", handleHealthCombined)

	// Gitcache routes.
	s.mux.HandleFunc("/archive", handleArchive)
	s.mux.HandleFunc("/repos", handleRepos)
	s.mux.HandleFunc("/artifacts/", handleArtifacts)
	s.mux.HandleFunc("/file", handleFile)
	s.mux.HandleFunc("/tree-hash", handleTreeHash)
	s.mux.HandleFunc("/branch-contains", handleBranchContains)
	s.mux.HandleFunc("/bin/", requireToken(handleBin))
	s.mux.HandleFunc("/cache/", requireToken(handleCache))
	s.mux.HandleFunc("/upload", requireToken(handleUpload))
	s.mux.HandleFunc("/uploads/", requireToken(handleUploadDownload))
	s.mux.HandleFunc("/sync/negotiate", requireToken(handleSyncNegotiate))
	s.mux.HandleFunc("/sync/seed", requireToken(handleSyncSeed))
	s.mux.HandleFunc("/git/register", handleGitRegister)
	s.mux.HandleFunc("/git/refresh", handleGitRefresh)
	s.mux.HandleFunc("/git/", handleGit)

	// Proxy routes (package registry cache).
	s.mux.HandleFunc("/proxy/", handleProxy)
	s.mux.HandleFunc("/stats", handleProxyStats)

	s.mux.Handle("/metrics", s.tel.PromHandler)

	s.http = &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(s.mux, "sparkwing-cache"),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // archives can be large
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

// Run starts the background fetch + proxy-cleanup loops, serves the
// HTTP mux, and blocks until ctx is cancelled. On cancellation it
// gracefully drains in-flight requests (30s timeout) and waits for
// every background goroutine to terminate before returning.
func (s *Server) Run(ctx context.Context) error {
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		backgroundFetchLoop(ctx, s.cfg.FetchInterval)
	}()
	go func() {
		defer s.wg.Done()
		proxyCleanupLoop(ctx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("sparkwing-cache listening on %s (proxy cache: %s)", s.cfg.Addr, s.cfg.ProxyDir)
		err := s.http.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Printf("sparkwing-cache shutting down (30s drain)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	s.tel.Shutdown(shutdownCtx)
	s.wg.Wait()
	log.Printf("sparkwing-cache stopped")
	return <-serveErr
}

// sleepCtx sleeps for d or returns false when ctx is cancelled.
// Background loops use this so a SIGTERM mid-sleep exits cleanly
// instead of blocking shutdown for the full interval.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
