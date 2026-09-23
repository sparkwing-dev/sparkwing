package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/cache"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-cache:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := cache.DefaultConfig()
	fs := flag.NewFlagSet("sparkwing-cache", flag.ExitOnError)

	fs.StringVar(&cfg.Addr, "addr",
		envOr("PORT_ADDR", ":"+envOr("PORT", trimColon(cfg.Addr))),
		"bind address (e.g. :8090). Default: $PORT_ADDR or :$PORT or :8090.")
	fs.StringVar(&cfg.DataDir, "data-dir",
		envOr("DATA_DIR", cfg.DataDir),
		"root of the gitcache filesystem layout (repos/, archives/, artifacts/, bins/, cache/, uploads/). Falls back to $DATA_DIR.")
	fs.StringVar(&cfg.ProxyDir, "proxy-cache-dir",
		envOr("PROXY_CACHE_DIR", ""),
		"root of the package-registry proxy cache. Empty means $DATA_DIR/proxy. Falls back to $PROXY_CACHE_DIR.")
	fs.DurationVar(&cfg.FetchInterval, "fetch-interval",
		envDuration("FETCH_INTERVAL", cfg.FetchInterval),
		"cadence of the keep-warm pass, which refreshes only mirrors a request touched in the last hour. Zero, the default, turns the pass off, leaving every mirror to refresh when a clone reads its refs. Falls back to $FETCH_INTERVAL.")
	fs.DurationVar(&cfg.FetchFreshWindow, "fetch-fresh-window",
		envDuration("FETCH_FRESH_WINDOW", cfg.FetchFreshWindow),
		"how long a successful mirror fetch lets request handlers skip their own fetch, which bounds what a caller can spend: at most one origin fetch per repository per window. Negative disables the throttle. Falls back to $FETCH_FRESH_WINDOW.")
	fs.DurationVar(&cfg.RecloneCooldown, "reclone-cooldown",
		envDuration("RECLONE_COOLDOWN", cfg.RecloneCooldown),
		"minimum gap between /archive recovery reclones, and between clone-if-missing attempts, for the same repo. Negative disables the cooldown. Falls back to $RECLONE_COOLDOWN.")
	fs.DurationVar(&cfg.ProxyCacheTTL, "proxy-cache-ttl",
		envDuration("PROXY_CACHE_TTL", cfg.ProxyCacheTTL),
		"max age of mutable proxy entries before re-fetching upstream. Falls back to $PROXY_CACHE_TTL.")
	fs.DurationVar(&cfg.ProxyMaxAge, "proxy-max-age",
		envDuration("PROXY_MAX_AGE", cfg.ProxyMaxAge),
		"cleanup threshold for immutable proxy entries (content-addressed files). Falls back to $PROXY_MAX_AGE.")
	fs.StringVar(&cfg.PublicURL, "public-url",
		envOr("SPARKWING_CACHE_PUBLIC_URL", cfg.PublicURL),
		"base URL clients use to reach this cache (e.g. http://sparkwing-cache.sparkwing.svc.cluster.local): a scheme and host, optionally with a /proxy path. Registry bodies are rewritten against it and the rewritten copy is cached, so it is correct only when every client dials the same address. Changing it leaves cached mutable entries on the old value until their TTL expires. Empty rewrites each response from its own request Host and caches the upstream body untouched. Falls back to $SPARKWING_CACHE_PUBLIC_URL.")
	fs.BoolVar(&cfg.TrustForwardedHost, "trust-forwarded-host",
		envBool("SPARKWING_CACHE_TRUST_FORWARDED_HOST", cfg.TrustForwardedHost),
		"honor X-Forwarded-Host and X-Forwarded-Proto when rewriting registry bodies from the request, taking the right-most element of each. Only safe when a reverse proxy is the only route to this port, and inert when --public-url is set. Falls back to $SPARKWING_CACHE_TRUST_FORWARDED_HOST.")
	fs.StringVar(&cfg.APIToken, "api-token",
		envOr("SPARKWING_API_TOKEN", cfg.APIToken),
		"bearer token required on the git, blob, artifact, and sync endpoints. Required unless --allow-unauthenticated is set. Falls back to $SPARKWING_API_TOKEN.")
	fs.StringVar(&cfg.GrantKey, "grant-key",
		envOr(authwire.CacheGrantKeyEnv, cfg.GrantKey),
		"key that verifies cache grants, the one the controller signs them with. No runner holds it, and it "+
			"must differ from --api-token. Empty accepts no grants. Falls back to $"+authwire.CacheGrantKeyEnv+".")
	fs.BoolVar(&cfg.AllowUnauthenticated, "allow-unauthenticated",
		envBool("SPARKWING_CACHE_ALLOW_UNAUTHENTICATED", cfg.AllowUnauthenticated),
		"start without a bearer token, leaving the git, blob, artifact, and sync endpoints open to anyone who can reach the port. Falls back to $SPARKWING_CACHE_ALLOW_UNAUTHENTICATED.")
	fs.StringVar(&cfg.AutoRegisterRepos, "auto-register-repos",
		envOr("GITCACHE_REPOS", cfg.AutoRegisterRepos),
		"comma-separated name=url pairs cloned into the gitcache on startup. Falls back to $GITCACHE_REPOS.")
	fs.StringVar(&cfg.SSHKeyDir, "ssh-key-dir",
		envOr("SSH_KEY_DIR", cfg.SSHKeyDir),
		"directory containing the SSH key + known_hosts (typically a k8s secret mount). Falls back to $SSH_KEY_DIR.")
	fs.DurationVar(&cfg.WorkspaceSeedMaxAge, "workspace-seed-max-age",
		envDuration("WORKSPACE_SEED_MAX_AGE", cfg.WorkspaceSeedMaxAge),
		"how long a working-tree snapshot ref is retained before the next seed expires it. Negative disables expiry. Falls back to $WORKSPACE_SEED_MAX_AGE.")
	fs.Int64Var(&cfg.MaxArtifactBytes, "max-artifact-bytes",
		envInt64("SPARKWING_CACHE_MAX_ARTIFACT_BYTES", cfg.MaxArtifactBytes),
		"size cap for one uploaded artifact; a larger upload is refused with 413 naming the cap. 0 accepts an artifact of any size. Falls back to $SPARKWING_CACHE_MAX_ARTIFACT_BYTES.")
	fs.Int64Var(&cfg.MaxCacheArchiveBytes, "max-cache-archive-bytes",
		envInt64("SPARKWING_CACHE_MAX_ARCHIVE_BYTES", cfg.MaxCacheArchiveBytes),
		"size cap for one stored dependency archive; a larger upload is refused with 413 naming the cap. 0 accepts an archive of any size. Falls back to $SPARKWING_CACHE_MAX_ARCHIVE_BYTES.")
	fs.Int64Var(&cfg.MaxStoreBytes, "max-store-bytes",
		envInt64("SPARKWING_CACHE_MAX_STORE_BYTES", cfg.MaxStoreBytes),
		"stored bytes across the artifact, dependency-archive, upload, team and git mirror trees at or above which every "+
			"upload is refused with 507 naming the ceiling, until a measurement finds the store back "+
			"under it. 0, the default, leaves the store unlimited. Falls back to $SPARKWING_CACHE_MAX_STORE_BYTES.")
	fs.Int64Var(&cfg.MaxStoreObjects, "max-store-objects",
		envInt64("SPARKWING_CACHE_MAX_STORE_OBJECTS", cfg.MaxStoreObjects),
		"stored files across those trees at or above which every upload is refused with 507; 0 leaves the count unlimited. Falls back to $SPARKWING_CACHE_MAX_STORE_OBJECTS.")
	fs.Int64Var(&cfg.WarnStoreBytes, "warn-store-bytes",
		envInt64("SPARKWING_CACHE_WARN_STORE_BYTES", cfg.WarnStoreBytes),
		"stored bytes at which /health reports the store as warning, which refuses nothing. Falls back to $SPARKWING_CACHE_WARN_STORE_BYTES.")
	fs.Int64Var(&cfg.WarnStoreObjects, "warn-store-objects",
		envInt64("SPARKWING_CACHE_WARN_STORE_OBJECTS", cfg.WarnStoreObjects),
		"stored files at which /health reports the store as warning. Falls back to $SPARKWING_CACHE_WARN_STORE_OBJECTS.")
	fs.DurationVar(&cfg.StoreReconcile, "store-reconcile",
		envDuration("SPARKWING_CACHE_STORE_RECONCILE", cfg.StoreReconcile),
		"how often the service walks its stored trees and replaces the running count with the measurement. "+
			"Uploads are counted as they happen, so this walk is the only enumeration the ceiling costs; "+
			"0 measures once at startup. Falls back to $SPARKWING_CACHE_STORE_RECONCILE.")
	fs.StringVar(&cfg.BlobStore, "blob-store",
		envOr("SPARKWING_CACHE_BLOB_STORE", cfg.BlobStore),
		"s3://bucket/prefix that holds the binary, dependency-archive and artifact stores instead of the volume, "+
			"one teams/<team>/ namespace per team. Region and credentials come from the AWS default chain (IRSA on EKS); "+
			"$SPARKWING_S3_ENDPOINT points it at an S3-compatible store. Git mirrors, uploads and the registry proxy stay "+
			"on --data-dir. Empty keeps everything on the volume. Falls back to $SPARKWING_CACHE_BLOB_STORE.")
	fs.StringVar(&cfg.ControllerURL, "controller",
		envOr("SPARKWING_CONTROLLER_URL", cfg.ControllerURL),
		"controller URL the cache asks, with --api-token, what each team a grant names may store in --blob-store: "+
			"funded, free up to three quarters of the free allowance, or nothing. Without it every team a grant "+
			"names is held to the default free share. Falls back to $SPARKWING_CONTROLLER_URL.")
	fs.DurationVar(&cfg.UsageReconcile, "usage-reconcile",
		envDuration("SPARKWING_CACHE_USAGE_RECONCILE", cfg.UsageReconcile),
		"with --blob-store, how often the per-team count of the bucket is replaced by a listing of it. Writes and "+
			"deletes keep the count between listings, and it is saved to the bucket every five minutes, so a restart "+
			"does not list unless --grant-key is set, when the count holds teams to their free share and every start "+
			"lists. 0 lists only at such a start or when no saved count exists. Falls back to $SPARKWING_CACHE_USAGE_RECONCILE.")
	fs.BoolVar(&cfg.DisableProxy, "disable-proxy", cfg.DisableProxy,
		"serve no registry proxy (/proxy/ and /stats). The proxy takes no credential, so a cache reachable "+
			"from outside the cluster sets this.")
	fs.Int64Var(&cfg.ProxyMaxBytes, "proxy-max-bytes",
		envInt64("SPARKWING_CACHE_PROXY_MAX_BYTES", cfg.ProxyMaxBytes),
		"size cap for the registry proxy's directory; past it the least recently served entries are evicted, "+
			"which costs one upstream fetch each. 0 leaves the proxy unbounded. Falls back to $SPARKWING_CACHE_PROXY_MAX_BYTES.")
	fs.IntVar(&cfg.GitForkLimit, "git-fork-limit",
		envInt("SPARKWING_GITCACHE_CONCURRENCY", cfg.GitForkLimit),
		"max concurrent git subprocesses. Falls back to $SPARKWING_GITCACHE_CONCURRENCY.")
	readEgress := egress.Bind(fs, os.Getenv, egress.ServiceCache, egress.CacheSurfaces)
	_ = fs.Parse(args)

	egressCfg, named, err := readEgress()
	if err != nil {
		return err
	}
	cfg.EgressDailyAlarmBytes = egressCfg.GlobalDailyAlarmBytes
	cfg.EgressDailyCapBytes = egressDailyCap(cfg, egressCfg, named)

	srv, err := cache.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx)
}

// safety: a cache that verifies grants serves more than one team, and its
// proxy and blob downloads are what an abusive team churns, so it starts
// with a finite daily cap unless the operator named one, zero included.
func egressDailyCap(cfg cache.Config, egressCfg egress.Config, named egress.Named) int64 {
	if cfg.GrantKey != "" && !named.DailyCapBytes {
		return cache.DefaultMultiTeamEgressDailyCapBytes
	}
	return egressCfg.GlobalDailyCapBytes
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	if v := os.Getenv(name); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// safety: a bound the operator misspelled must not decay into "unlimited" in
// silence, so the service says which value it could not read.
func envInt64(name string, fallback int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		fmt.Fprintf(os.Stderr,
			"sparkwing-cache: %s=%q is not a byte count; keeping %d\n", name, v, fallback)
		return fallback
	}
	return n
}

func trimColon(s string) string {
	if len(s) > 0 && s[0] == ':' {
		return s[1:]
	}
	return s
}
