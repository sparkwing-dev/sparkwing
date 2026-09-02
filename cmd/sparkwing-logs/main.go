package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-logs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-logs", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:4345", "bind address")
	root := fs.String("root", "", "storage root; explicit roots keep shared/PVC creation modes subject to umask (default: private $SPARKWING_HOME/logs-service)")
	controllerURL := fs.String("controller", os.Getenv("SPARKWING_CONTROLLER_URL"),
		"controller URL used to resolve sw*_ tokens via /api/v1/auth/whoami; empty disables auth (env: SPARKWING_CONTROLLER_URL)")
	requireAuth := fs.Bool("require-auth", envTruthy("SPARKWING_REQUIRE_AUTH"),
		"refuse to start unless --controller is an absolute http(s) URL, "+
			"guarding against accidentally deploying a logs service that serves, forges, and "+
			"deletes every run's logs for anyone who can reach it. Leave unset "+
			"for laptop-local use.")

	defaults := logs.DefaultLimits()
	maxNodeBytes := fs.Int64("max-node-bytes", envInt64("SPARKWING_LOGS_MAX_NODE_BYTES", defaults.MaxNodeBytes),
		"stored-byte cap for one node's log; further appends land a truncation marker instead. "+
			"0 disables the cap (env: SPARKWING_LOGS_MAX_NODE_BYTES)")
	maxRunBytes := fs.Int64("max-run-bytes", envInt64("SPARKWING_LOGS_MAX_RUN_BYTES", defaults.MaxRunBytes),
		"stored-byte cap for all node logs in one run; 0 disables the cap "+
			"(env: SPARKWING_LOGS_MAX_RUN_BYTES)")
	minFreeBytes := fs.Int64("min-free-bytes", envInt64("SPARKWING_LOGS_MIN_FREE_BYTES", int64(defaults.MinFreeBytes)),
		"free space on the storage volume below which appends are rejected with 507; "+
			"0 disables the floor (env: SPARKWING_LOGS_MIN_FREE_BYTES)")
	retention := fs.Duration("retention", envDuration("SPARKWING_LOGS_RETENTION", defaults.Retention),
		"how long a run's logs survive after their last write; 0 keeps them forever "+
			"(env: SPARKWING_LOGS_RETENTION)")
	sweepInterval := fs.Duration("sweep-interval", envDuration("SPARKWING_LOGS_SWEEP_INTERVAL", defaults.SweepInterval),
		"how often the retention sweeper runs (env: SPARKWING_LOGS_SWEEP_INTERVAL)")
	searchMaxBytes := fs.Int64("search-max-bytes", envInt64("SPARKWING_LOGS_SEARCH_MAX_BYTES", defaults.SearchMaxBytes),
		"bytes one search request may read before it returns a truncated result; "+
			"0 disables the cap (env: SPARKWING_LOGS_SEARCH_MAX_BYTES)")
	searchTimeout := fs.Duration("search-timeout", envDuration("SPARKWING_LOGS_SEARCH_TIMEOUT", defaults.SearchTimeout),
		"how long one search request may scan before it returns a truncated result; "+
			"0 disables the deadline (env: SPARKWING_LOGS_SEARCH_TIMEOUT)")
	_ = fs.Parse(args)

	limits := logs.Limits{
		MaxNodeBytes:   nonNegative(*maxNodeBytes),
		MaxRunBytes:    nonNegative(*maxRunBytes),
		MinFreeBytes:   uint64(nonNegative(*minFreeBytes)),
		Retention:      *retention,
		SweepInterval:  *sweepInterval,
		SearchMaxBytes: nonNegative(*searchMaxBytes),
		SearchTimeout:  *searchTimeout,
	}

	if *requireAuth {
		if err := checkControllerURL(*controllerURL); err != nil {
			return err
		}
	}

	privateRoot := *root == ""
	if privateRoot {
		p, err := paths.DefaultPaths()
		if err != nil {
			return err
		}
		*root = filepath.Join(p.Root, "logs-service")
		if err := fssecure.EnsureDir(*root); err != nil {
			return fmt.Errorf("secure default storage root: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-logs"})
	defer func() { _ = tel.Shutdown(context.Background()) }()
	return logs.ServeWith(ctx, logs.ServeOptions{
		Root:          *root,
		Addr:          *addr,
		ControllerURL: *controllerURL,
		Private:       privateRoot,
		Limits:        &limits,
	})
}

func nonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func envInt64(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func envDuration(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// safety: --require-auth advertises auth on /api/v1/health, so a URL that resolves no token must not start.
func checkControllerURL(raw string) error {
	const remedy = "; point the logs service at a controller so it can " +
		"resolve caller tokens, or drop --require-auth for laptop-local use"
	if strings.TrimSpace(raw) == "" {
		return errors.New("--require-auth (SPARKWING_REQUIRE_AUTH) is set but " +
			"--controller (SPARKWING_CONTROLLER_URL) is empty" + remedy)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but "+
			"--controller (SPARKWING_CONTROLLER_URL) %q is not a URL: %w"+remedy, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but "+
			"--controller (SPARKWING_CONTROLLER_URL) %q is not an absolute http(s) URL"+remedy, raw)
	}
	return nil
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
