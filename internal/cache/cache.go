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

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/logutil"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

type Config struct {
	Addr string

	DataDir string

	ProxyDir string

	// FetchInterval is how often the keep-warm pass refreshes mirrors a
	// request touched in the last hour. Zero, the default, turns the pass off,
	// and every mirror then refreshes when a clone reads its refs.
	FetchInterval time.Duration

	FetchFreshWindow time.Duration

	RecloneCooldown time.Duration

	ProxyCacheTTL time.Duration

	ProxyMaxAge time.Duration

	PublicURL string

	TrustForwardedHost bool

	APIToken string

	// GrantKey verifies cache grants. The controller signs grants with the
	// same key, and no runner holds it. Empty accepts no grants. It must
	// differ from APIToken.
	GrantKey string

	AllowUnauthenticated bool

	AutoRegisterRepos string

	SSHKeyDir string

	GitForkLimit int

	WorkspaceSeedMaxAge time.Duration

	MaxArtifactBytes int64

	MaxCacheArchiveBytes int64

	MaxStoreBytes int64

	MaxStoreObjects int64

	WarnStoreBytes int64

	WarnStoreObjects int64

	StoreReconcile time.Duration
	// BlobStore, an s3://bucket/prefix URL, moves the binary,
	// dependency-archive and artifact stores off the volume into that
	// bucket, one teams/<team>/ namespace per team. Empty keeps them on
	// the volume. Credentials and region come from the AWS default chain.
	BlobStore string
	// ControllerURL is where the cache asks, with APIToken, what each
	// team may store in BlobStore. Empty holds every team a grant names to
	// the default free share.
	ControllerURL string
	// UsageReconcile is how often the per-team count of the bucket is
	// replaced by a listing. Writes and deletes keep it between listings.
	UsageReconcile time.Duration
	// DisableProxy serves no registry proxy. The proxy takes no
	// credential, so a cache published outside the cluster sets it.
	DisableProxy bool
	// MetricsAddr moves /metrics and the proxy's /stats off Addr onto a
	// listener of their own, so a cache published through an ingress on
	// Addr exposes neither. Empty serves both on Addr.
	MetricsAddr string
	// ProxyMaxBytes caps the registry proxy's directory; past it the least
	// recently served entries are evicted. Zero leaves it unbounded.
	ProxyMaxBytes int64
	// EgressDailyAlarmBytes raises the egress alarm, which health
	// reports, once this pod has sent this many bytes in a UTC day. It
	// refuses nothing. Zero is off.
	EgressDailyAlarmBytes int64
	// EgressDailyCapBytes refuses every metered download with 429 once
	// this pod has sent this many bytes in a UTC day, until the day
	// rolls. It bounds the pod's bill whoever the callers are. Zero is
	// off.
	EgressDailyCapBytes int64
	// TeamDailyDownloadFreeBytes and TeamDailyDownloadFundedBytes cap what
	// the cache serves one team's grants in a UTC day, by whether the team
	// pays; past it the team's downloads are refused with 429 until the day
	// rolls. The operator's team and token are exempt. Zero is off.
	TeamDailyDownloadFreeBytes   int64
	TeamDailyDownloadFundedBytes int64
}

func DefaultConfig() Config {
	return Config{
		Addr:             ":8090",
		DataDir:          "/data",
		ProxyDir:         "/data/proxy",
		FetchFreshWindow: 10 * time.Second,
		RecloneCooldown:  1 * time.Hour,
		ProxyCacheTTL:    10 * time.Minute,
		ProxyMaxAge:      7 * 24 * time.Hour,
		SSHKeyDir:        "/etc/ssh-key",
		GitForkLimit:     4,

		MaxArtifactBytes:     DefaultMaxArtifactBytes,
		MaxCacheArchiveBytes: DefaultMaxCacheArchiveBytes,
		StoreReconcile:       objectguard.DefaultCeilingReconcile,
		UsageReconcile:       24 * time.Hour,

		WorkspaceSeedMaxAge: 24 * time.Hour,
		ProxyMaxBytes:       DefaultProxyMaxBytes,

		TeamDailyDownloadFreeBytes:   DefaultTeamDailyDownloadFreeBytes,
		TeamDailyDownloadFundedBytes: DefaultTeamDailyDownloadFundedBytes,
	}
}

// DefaultTeamDailyDownloadFreeBytes and DefaultTeamDailyDownloadFundedBytes
// are what the cache serves one team's grants in a UTC day, by whether the
// team pays, when the operator named no cap.
const (
	DefaultTeamDailyDownloadFreeBytes   int64 = 5 << 30
	DefaultTeamDailyDownloadFundedBytes int64 = 50 << 30
)

// DefaultMultiTeamEgressDailyCapBytes is the daily egress cap a cache that
// verifies grants starts with when the operator named none: what one pod may
// send in a UTC day before every metered download is refused until the day
// rolls.
const DefaultMultiTeamEgressDailyCapBytes int64 = 200 << 30

const serverReadTimeout = 30 * time.Second

type Server struct {
	cfg     Config
	tel     *otelutil.Telemetry
	mux     *http.ServeMux
	handler http.Handler
	http    *http.Server
	// metrics serves /metrics and /stats when Config.MetricsAddr is set.
	metrics     http.Handler
	metricsHTTP *http.Server
	wg          sync.WaitGroup
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
	cfg.GrantKey = strings.TrimSpace(cfg.GrantKey)
	if cfg.GrantKey != "" && cfg.GrantKey == cfg.APIToken {
		return nil, fmt.Errorf("cache: the grant key (--grant-key or $%s) is the operator token; "+
			"give it a secret of its own, because whoever holds the key signs access to every team's tree",
			authwire.CacheGrantKeyEnv)
	}
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
	if cfg.FetchFreshWindow == 0 {
		cfg.FetchFreshWindow = 10 * time.Second
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
	publicBase, err := normalizeProxyPublicBase(cfg.PublicURL)
	if err != nil {
		return nil, err
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
	if cfg.MaxArtifactBytes < 0 || cfg.MaxCacheArchiveBytes < 0 {
		return nil, fmt.Errorf("cache: an object size cap must not be negative; pass 0 to accept an object of any size")
	}
	if cfg.MaxStoreBytes < 0 || cfg.MaxStoreObjects < 0 || cfg.WarnStoreBytes < 0 || cfg.WarnStoreObjects < 0 {
		return nil, fmt.Errorf("cache: a store ceiling must not be negative; pass 0 to leave the store unlimited")
	}
	if cfg.TeamDailyDownloadFreeBytes < 0 || cfg.TeamDailyDownloadFundedBytes < 0 {
		return nil, fmt.Errorf("cache: a team daily download cap must not be negative; pass 0 to turn it off")
	}
	if cfg.StoreReconcile < 0 {
		return nil, fmt.Errorf("cache: --store-reconcile must not be negative; pass 0 to measure the store once at startup")
	}

	dataRoot = cfg.DataDir
	repoDir = filepath.Join(cfg.DataDir, "repos")
	archDir = filepath.Join(cfg.DataDir, "archives")
	artifactsDir = filepath.Join(cfg.DataDir, "artifacts")
	binsDir = filepath.Join(cfg.DataDir, "bins")
	cacheDir = filepath.Join(cfg.DataDir, "cache")
	uploadsDir = filepath.Join(cfg.DataDir, "uploads")
	teamsDir = filepath.Join(cfg.DataDir, "teams")
	namesFile = filepath.Join(cfg.DataDir, "repo-names.json")
	proxyDir = cfg.ProxyDir
	proxyCacheTTL = cfg.ProxyCacheTTL
	proxyMaxAge = cfg.ProxyMaxAge
	proxyPublicBase = publicBase
	proxyTrustForwardedHost = cfg.TrustForwardedHost
	apiToken = cfg.APIToken
	grantKey = cfg.GrantKey
	sshKeyDir = cfg.SSHKeyDir
	autoRegisterReposSpec = cfg.AutoRegisterRepos
	fetchFreshWindow = cfg.FetchFreshWindow
	recloneCooldown = cfg.RecloneCooldown
	gitForkSem = make(chan struct{}, cfg.GitForkLimit)
	workspaceSeedMaxAge = cfg.WorkspaceSeedMaxAge
	maxArtifactBytes = cfg.MaxArtifactBytes
	maxCacheArchiveBytes = cfg.MaxCacheArchiveBytes
	if cfg.ProxyMaxBytes < 0 {
		return nil, fmt.Errorf("cache: --proxy-max-bytes must not be negative; pass 0 to leave the proxy unbounded")
	}
	proxyMaxBytes = cfg.ProxyMaxBytes
	storeCeiling = objectguard.NewCeiling(objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{
			MaxBytes:    cfg.MaxStoreBytes,
			MaxObjects:  cfg.MaxStoreObjects,
			WarnBytes:   cfg.WarnStoreBytes,
			WarnObjects: cfg.WarnStoreObjects,
		},
		Reconcile: cfg.StoreReconcile,
		Subject:   storeCeilingSubject,
		Remedy:    storeCeilingRemedy,
	})

	blobStore, blobQuota = nil, nil
	if cfg.BlobStore != "" {
		octx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		store, err := openBlobStore(octx, cfg.BlobStore)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("cache: --blob-store: %w", err)
		}
		blobStore = store
		log.Printf("sparkwing-cache keeps binaries, dependency archives and artifacts in s3://%s/%s",
			store.Bucket(), store.Prefix())
		blobQuota = newBlobQuota(store, cfg)
		// safety: the count holds teams to their share, so it is restored
		// before the handler exists; a cache that cannot count its bucket
		// does not start, rather than admit uploads against an empty count.
		if cfg.GrantKey != "" {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err := store.Restore(rctx, cfg.UsageReconcile)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("cache: count the blob store before serving: %w", err)
			}
		}
	}

	log.Printf("sparkwing-cache caps one artifact at %d bytes and one dependency archive at %d bytes, "+
		"and the whole store at %d bytes / %d objects (0 means no cap)",
		maxArtifactBytes, maxCacheArchiveBytes, cfg.MaxStoreBytes, cfg.MaxStoreObjects)

	for _, d := range []string{repoDir, archDir, artifactsDir, binsDir, cacheDir, uploadsDir, teamsDir, proxyDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("cache: mkdir %s: %w", d, err)
		}
	}
	if proxyPublicBase != "" {
		log.Printf("sparkwing-cache rewrites proxied registry bodies against %s", proxyPublicBase)
	} else {
		log.Printf("sparkwing-cache rewrites proxied registry bodies per request from the Host header; " +
			"set --public-url (or $SPARKWING_CACHE_PUBLIC_URL) to rewrite against one fixed base")
	}

	egressCfg := egress.Config{
		GlobalDailyAlarmBytes: cfg.EgressDailyAlarmBytes,
		GlobalDailyCapBytes:   cfg.EgressDailyCapBytes,
	}
	setEgressMeter(egressCfg)
	logEgressBudgets(egressCfg)
	setTeamDownloadCaps(cfg)
	if blobStore != nil {
		rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := restoreEgressDay(rctx); err != nil {
			log.Printf("warning: read the day's saved egress total: %v", err)
		}
		cancel()
	}

	loadRepoNames()
	initProxy()

	s := &Server{cfg: cfg}
	// bug: instruments bind to the meter provider current when they are created, so telemetry starts first.
	s.tel = otelutil.Init(context.Background(), otelutil.Config{ServiceName: "sparkwing-cache"})
	initGitcacheMetrics()
	initProxyMetrics()
	initStoreCeilingMetrics()
	if err := registerTeamDownloadMetric(otelutil.Meter("sparkwing-cache")); err != nil {
		log.Printf("warning: team download metric: %v", err)
	}
	if blobQuota != nil {
		if err := storagequota.RegisterMetric(otelutil.Meter("sparkwing-cache"), "cache", blobQuota); err != nil {
			log.Printf("warning: free storage metric: %v", err)
		}
	}
	if err := setupSSH(); err != nil {
		return nil, err
	}
	autoRegisterRepos()

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/health", handleHealthCombined)

	s.mux.HandleFunc("/archive", requireToken(metered(egress.ClassArtifact, handleArchive)))
	s.mux.HandleFunc("/repos", requireToken(handleRepos))
	s.mux.HandleFunc("/artifacts/", requireCaller(metered(egress.ClassArtifact, handleArtifacts)))
	s.mux.HandleFunc("/file", requireToken(metered(egress.ClassArtifact, handleFile)))
	s.mux.HandleFunc("/tree-hash", requireToken(handleTreeHash))
	s.mux.HandleFunc("/branch-contains", requireToken(handleBranchContains))
	s.mux.HandleFunc("/bin/", requireCaller(metered(egress.ClassArtifact, handleBin)))
	s.mux.HandleFunc("/cache/", requireCaller(metered(egress.ClassArtifact, handleCache)))
	s.mux.HandleFunc("/upload", requireToken(handleUpload))
	s.mux.HandleFunc("/admin/store-ceiling/thaw", requireToken(handleStoreCeilingThaw))
	s.mux.HandleFunc("/admin/store-ceiling/measure", requireToken(handleStoreCeilingMeasure))
	s.mux.HandleFunc("/admin/teams/", requireToken(handleDeleteTeamTree))
	s.mux.HandleFunc("/admin/usage", requireToken(handleBlobUsage))
	s.mux.HandleFunc("/uploads/", requireToken(metered(egress.ClassArtifact, handleUploadDownload)))
	s.mux.HandleFunc("/sync/negotiate", requireToken(handleSyncNegotiate))
	s.mux.HandleFunc("/sync/seed", requireToken(handleSyncSeed))
	s.mux.HandleFunc("/git/register", requireCaller(handleGitRegister))
	s.mux.HandleFunc("/git/refresh", requireToken(handleGitRefresh))
	s.mux.HandleFunc("/git/", requireCaller(metered(egress.ClassGit, handleGit)))

	// safety: runner pods reach the proxy with no credential, so it stays
	// open; it is served inside the cluster only, and the daily egress cap
	// bounds what any caller churns through it.
	operator := s.mux
	if cfg.MetricsAddr != "" {
		operator = http.NewServeMux()
		s.metrics = operator
		s.metricsHTTP = &http.Server{
			Addr:              cfg.MetricsAddr,
			Handler:           operator,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      30 * time.Second,
		}
	}
	if !cfg.DisableProxy {
		s.mux.HandleFunc("/proxy/", metered(egress.ClassGit, handleProxy))
		operator.HandleFunc("/stats", handleProxyStats)
	}
	operator.Handle("/metrics", s.tel.PromHandler)

	s.handler = withSecurityHeaders(s.mux)

	s.http = &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(s.handler, "sparkwing-cache"),
		ReadTimeout:  serverReadTimeout,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

// Handler serves the cache's routes without the listener, background fetch or
// store reconcile that Run starts, so a caller can mount the cache on its own
// listener.
func (s *Server) Handler() http.Handler { return s.handler }

// MetricsHandler serves /metrics and /stats when Config.MetricsAddr moved
// them off the main listener, and is nil otherwise.
func (s *Server) MetricsHandler() http.Handler { return s.metrics }

func (s *Server) Run(ctx context.Context) error {
	setMeasureContext(ctx)
	if blobStore != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			report := func(op string, err error) { log.Printf("warning: blob store %s: %v", op, err) }
			if s.cfg.GrantKey != "" {
				blobStore.Keep(ctx, s.cfg.UsageReconcile, 5*time.Minute, report)
				return
			}
			blobStore.Maintain(ctx, s.cfg.UsageReconcile, 5*time.Minute, report)
		}()
	}
	measureStore(ctx)
	if blobStore != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			egressDayLoop(ctx)
		}()
	}
	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		storeCeilingLoop(ctx)
	}()
	go func() {
		defer s.wg.Done()
		backgroundFetchLoop(ctx, s.cfg.FetchInterval)
	}()
	go func() {
		defer s.wg.Done()
		proxyCleanupLoop(ctx)
	}()

	serveErr := make(chan error, 2)
	if s.metricsHTTP != nil {
		go func() {
			log.Printf("sparkwing-cache serves /metrics and /stats on %s", s.cfg.MetricsAddr)
			err := s.metricsHTTP.ListenAndServe()
			if err == http.ErrServerClosed {
				return
			}
			serveErr <- fmt.Errorf("metrics listener: %w", err)
		}()
	}
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
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if s.metricsHTTP != nil {
		if err := s.metricsHTTP.Shutdown(shutdownCtx); err != nil {
			log.Printf("metrics listener shutdown: %v", err)
		}
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
