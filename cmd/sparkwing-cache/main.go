// Command sparkwing-cache is the cache pod's entry point: an HTTP
// gitcache, blob/artifact store, upload sync, and pass-through
// package-registry proxy. The business logic lives in
// internal/cache; this file only parses flags and calls Run.
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

	// Every flag defaults to the env-var value when set, otherwise
	// the legacy hard-coded default. Preserves existing k8s
	// ConfigMap-style env-var configs while letting new deploys
	// use flags.
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
	fs.DurationVar(&cfg.ProxyCacheTTL, "proxy-cache-ttl",
		envDuration("PROXY_CACHE_TTL", cfg.ProxyCacheTTL),
		"max age of mutable proxy entries before re-fetching upstream. Falls back to $PROXY_CACHE_TTL.")
	fs.DurationVar(&cfg.ProxyMaxAge, "proxy-max-age",
		envDuration("PROXY_MAX_AGE", cfg.ProxyMaxAge),
		"cleanup threshold for immutable proxy entries (content-addressed files). Falls back to $PROXY_MAX_AGE.")
	fs.StringVar(&cfg.APIToken, "api-token",
		envOr("SPARKWING_API_TOKEN", cfg.APIToken),
		"bearer token required on external write endpoints. Empty disables auth. Falls back to $SPARKWING_API_TOKEN.")
	fs.StringVar(&cfg.AutoRegisterRepos, "auto-register-repos",
		envOr("GITCACHE_REPOS", cfg.AutoRegisterRepos),
		"comma-separated name=url pairs cloned into the gitcache on startup. Falls back to $GITCACHE_REPOS.")
	fs.StringVar(&cfg.SSHKeyDir, "ssh-key-dir",
		envOr("SSH_KEY_DIR", cfg.SSHKeyDir),
		"directory containing the SSH key + known_hosts (typically a k8s secret mount). Falls back to $SSH_KEY_DIR.")
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

// trimColon strips a leading `:` so PORT (which is just a number)
// can be composed back into a full bind address.
func trimColon(s string) string {
	if len(s) > 0 && s[0] == ':' {
		return s[1:]
	}
	return s
}
