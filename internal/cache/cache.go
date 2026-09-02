package cache

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/sparkwing-dev/sparkwing/internal/logutil"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

type Config struct {
	Addr string

	DataDir string

	ProxyDir string

	FetchInterval time.Duration

	FetchFreshWindow time.Duration

	RecloneCooldown time.Duration

	ProxyCacheTTL time.Duration

	ProxyMaxAge time.Duration

	APIToken string

	AllowUnauthenticated bool

	AutoRegisterRepos string

	SSHKeyDir string

	GitForkLimit int

	WorkspaceSeedMaxAge time.Duration
}

func DefaultConfig() Config {
	return Config{
		Addr:             ":8090",
		DataDir:          "/data",
		ProxyDir:         "/data/proxy",
		FetchInterval:    30 * time.Second,
		FetchFreshWindow: 15 * time.Second,
		RecloneCooldown:  1 * time.Hour,
		ProxyCacheTTL:    10 * time.Minute,
		ProxyMaxAge:      7 * 24 * time.Hour,
		SSHKeyDir:        "/etc/ssh-key",
		GitForkLimit:     4,

		WorkspaceSeedMaxAge: 24 * time.Hour,
	}
}

type Server struct {
	cfg     Config
	tel     *otelutil.Telemetry
	mux     *http.ServeMux
	handler http.Handler
	http    *http.Server
	wg      sync.WaitGroup
}

func New(cfg Config) (*Server, error) {
	logutil.Init()
	if cfg.Addr == "" {
		cfg.Addr = ":8090"
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("cache: DataDir is required")
	}
	// safety: a Secret key holding only a newline must not count as a configured credential.
	cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	if cfg.APIToken == "" {
		if !cfg.AllowUnauthenticated {
			return nil, fmt.Errorf("cache: an API token is required: set --api-token (or $SPARKWING_API_TOKEN), " +
				"or pass --allow-unauthenticated to serve the git, blob, artifact, and sync endpoints to anyone who can reach the port")
		}
		log.Printf("WARNING: sparkwing-cache is serving the git, blob, artifact, and sync endpoints without authentication (--allow-unauthenticated)")
	} else {
		log.Printf("sparkwing-cache requires a bearer token on the git, blob, artifact, and sync endpoints")
	}
	if cfg.ProxyDir == "" {
		cfg.ProxyDir = filepath.Join(cfg.DataDir, "proxy")
	}
	if cfg.FetchInterval <= 0 {
		cfg.FetchInterval = 30 * time.Second
	}

	if cfg.FetchFreshWindow == 0 {
		cfg.FetchFreshWindow = 15 * time.Second
	}
	if cfg.RecloneCooldown == 0 {
		cfg.RecloneCooldown = 1 * time.Hour
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
	if cfg.WorkspaceSeedMaxAge == 0 {
		cfg.WorkspaceSeedMaxAge = 24 * time.Hour
	}

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
	fetchFreshWindow = cfg.FetchFreshWindow
	recloneCooldown = cfg.RecloneCooldown
	gitForkSem = make(chan struct{}, cfg.GitForkLimit)
	workspaceSeedMaxAge = cfg.WorkspaceSeedMaxAge

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

	s.mux.HandleFunc("/archive", requireToken(handleArchive))
	s.mux.HandleFunc("/repos", requireToken(handleRepos))
	s.mux.HandleFunc("/artifacts/", requireToken(handleArtifacts))
	s.mux.HandleFunc("/file", requireToken(handleFile))
	s.mux.HandleFunc("/tree-hash", requireToken(handleTreeHash))
	s.mux.HandleFunc("/branch-contains", requireToken(handleBranchContains))
	s.mux.HandleFunc("/bin/", requireToken(handleBin))
	s.mux.HandleFunc("/cache/", requireToken(handleCache))
	s.mux.HandleFunc("/upload", requireToken(handleUpload))
	s.mux.HandleFunc("/uploads/", requireToken(handleUploadDownload))
	s.mux.HandleFunc("/sync/negotiate", requireToken(handleSyncNegotiate))
	s.mux.HandleFunc("/sync/seed", requireToken(handleSyncSeed))
	s.mux.HandleFunc("/git/register", requireToken(handleGitRegister))
	s.mux.HandleFunc("/git/refresh", requireToken(handleGitRefresh))
	s.mux.HandleFunc("/git/", requireToken(handleGit))

	s.mux.HandleFunc("/proxy/", handleProxy)
	s.mux.HandleFunc("/stats", handleProxyStats)

	s.mux.Handle("/metrics", s.tel.PromHandler)

	s.handler = withSecurityHeaders(s.mux)

	s.http = &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(s.handler, "sparkwing-cache"),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

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
	_ = s.tel.Shutdown(shutdownCtx)
	s.wg.Wait()
	log.Printf("sparkwing-cache stopped")
	return <-serveErr
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: cache bodies are caller-supplied, so no response may be content-sniffed into a script.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

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
