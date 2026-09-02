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

	"github.com/sparkwing-dev/sparkwing/internal/cache"
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
		"cadence of the background gitcache fetch loop. Falls back to $FETCH_INTERVAL.")
	fs.DurationVar(&cfg.FetchFreshWindow, "fetch-fresh-window",
		envDuration("FETCH_FRESH_WINDOW", cfg.FetchFreshWindow),
		"how long a successful mirror fetch lets request handlers skip their own fetch. Negative disables the throttle. Falls back to $FETCH_FRESH_WINDOW.")
	fs.DurationVar(&cfg.RecloneCooldown, "reclone-cooldown",
		envDuration("RECLONE_COOLDOWN", cfg.RecloneCooldown),
		"minimum gap between /archive recovery reclones of the same repo. Negative disables the cooldown. Falls back to $RECLONE_COOLDOWN.")
	fs.DurationVar(&cfg.ProxyCacheTTL, "proxy-cache-ttl",
		envDuration("PROXY_CACHE_TTL", cfg.ProxyCacheTTL),
		"max age of mutable proxy entries before re-fetching upstream. Falls back to $PROXY_CACHE_TTL.")
	fs.DurationVar(&cfg.ProxyMaxAge, "proxy-max-age",
		envDuration("PROXY_MAX_AGE", cfg.ProxyMaxAge),
		"cleanup threshold for immutable proxy entries (content-addressed files). Falls back to $PROXY_MAX_AGE.")
	fs.StringVar(&cfg.PublicURL, "public-url",
		envOr("SPARKWING_CACHE_PUBLIC_URL", cfg.PublicURL),
		"base URL clients use to reach this cache (e.g. http://sparkwing-cache.sparkwing.svc.cluster.local). Registry bodies are rewritten against it and the rewritten copy is cached. Empty rewrites each response from its own request Host and caches the upstream body untouched. Falls back to $SPARKWING_CACHE_PUBLIC_URL.")
	fs.BoolVar(&cfg.TrustForwardedHost, "trust-forwarded-host",
		envBool("SPARKWING_CACHE_TRUST_FORWARDED_HOST", cfg.TrustForwardedHost),
		"honor X-Forwarded-Host and X-Forwarded-Proto when rewriting registry bodies from the request. Only safe when a reverse proxy is the only route to this port. Falls back to $SPARKWING_CACHE_TRUST_FORWARDED_HOST.")
	fs.StringVar(&cfg.APIToken, "api-token",
		envOr("SPARKWING_API_TOKEN", cfg.APIToken),
		"bearer token required on the git, blob, artifact, and sync endpoints. Required unless --allow-unauthenticated is set. Falls back to $SPARKWING_API_TOKEN.")
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
	fs.IntVar(&cfg.GitForkLimit, "git-fork-limit",
		envInt("SPARKWING_GITCACHE_CONCURRENCY", cfg.GitForkLimit),
		"max concurrent git subprocesses. Falls back to $SPARKWING_GITCACHE_CONCURRENCY.")
	_ = fs.Parse(args)

	srv, err := cache.New(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Run(ctx)
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

func trimColon(s string) string {
	if len(s) > 0 && s[0] == ':' {
		return s[1:]
	}
	return s
}
