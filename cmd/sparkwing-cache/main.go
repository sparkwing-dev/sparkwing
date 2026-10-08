package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/cache"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
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
	fs := flag.NewFlagSet("sparkwing-cache", flag.ContinueOnError)

	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "bind address (e.g. :8090)")
	fs.StringVar(&cfg.DataDir, "data-dir",
		cfg.DataDir,
		"root of the gitcache filesystem layout (repos/, bins/, cache/, teams/).")
	fs.StringVar(&cfg.ProxyDir, "proxy-cache-dir",
		"",
		"root of the package-registry proxy cache. Empty means <data-dir>/proxy.")
	fs.DurationVar(&cfg.FetchInterval, "fetch-interval",
		cfg.FetchInterval,
		"cadence of the keep-warm pass, which refreshes only mirrors a request touched in the last hour. Zero, the default, turns the pass off, leaving every mirror to refresh when a clone reads its refs.")
	fs.DurationVar(&cfg.FetchFreshWindow, "fetch-fresh-window",
		cfg.FetchFreshWindow,
		"how long a successful mirror fetch lets request handlers skip their own fetch, which bounds what a caller can spend: at most one origin fetch per repository per window. Negative disables the throttle.")
	fs.DurationVar(&cfg.RecloneCooldown, "reclone-cooldown",
		cfg.RecloneCooldown,
		"minimum gap between clone-if-missing attempts for the same repo. Negative disables the cooldown.")
	fs.DurationVar(&cfg.ProxyCacheTTL, "proxy-cache-ttl",
		cfg.ProxyCacheTTL,
		"max age of mutable proxy entries before re-fetching upstream.")
	fs.DurationVar(&cfg.ProxyMaxAge, "proxy-max-age",
		cfg.ProxyMaxAge,
		"cleanup threshold for immutable proxy entries (content-addressed files).")
	fs.StringVar(&cfg.PublicURL, "public-url",
		cfg.PublicURL,
		"base URL clients use to reach this cache (e.g. http://sparkwing-cache.sparkwing.svc.cluster.local): a scheme and host, optionally with a /proxy path. Registry bodies are rewritten against it and the rewritten copy is cached, so it is correct only when every client dials the same address. Changing it leaves cached mutable entries on the old value until their TTL expires. Empty rewrites each response from its own request Host and caches the upstream body untouched.")
	fs.BoolVar(&cfg.TrustForwardedHost, "trust-forwarded-host",
		cfg.TrustForwardedHost,
		"honor X-Forwarded-Host and X-Forwarded-Proto when rewriting registry bodies from the request, taking the right-most element of each. Only safe when a reverse proxy is the only route to this port, and inert when --public-url is set.")
	credentialsDir := fs.String(credentials.FlagName, "",
		"directory holding the cache's secrets, one file each: "+authwire.CacheTokenCredential+", the bearer token the git and "+
			"blob endpoints require unless --allow-unauthenticated is set, and "+authwire.CacheGrantKeyCredential+", the key that "+
			"verifies cache grants, the one the controller signs them with. No runner holds the key, and it must differ "+
			"from the token. An absent file turns its feature off; an absent grant key accepts no grants.")
	fs.BoolVar(&cfg.AllowUnauthenticated, "allow-unauthenticated",
		cfg.AllowUnauthenticated,
		"start without a bearer token, leaving the git and blob endpoints open to anyone who can reach the port.")
	fs.StringVar(&cfg.AutoRegisterRepos, "auto-register-repos",
		cfg.AutoRegisterRepos,
		"comma-separated name=url pairs cloned into the gitcache on startup.")
	fs.StringVar(&cfg.SSHKeyDir, "ssh-key-dir",
		cfg.SSHKeyDir,
		"directory containing the SSH key + known_hosts (typically a k8s secret mount).")
	fs.Int64Var(&cfg.MaxCacheArchiveBytes, "max-cache-archive-bytes",
		cfg.MaxCacheArchiveBytes,
		"size cap for one stored dependency archive; a larger upload is refused with 413 naming the cap. 0 accepts an archive of any size.")
	fs.Int64Var(&cfg.MaxStoreBytes, "max-store-bytes",
		cfg.MaxStoreBytes,
		"stored bytes across the dependency-archive, team and git mirror trees at or above which every "+
			"upload is refused with 507 naming the ceiling, until a measurement finds the store back "+
			"under it. 0, the default, leaves the store unlimited.")
	fs.Int64Var(&cfg.MaxStoreObjects, "max-store-objects",
		cfg.MaxStoreObjects,
		"stored files across those trees at or above which every upload is refused with 507; 0 leaves the count unlimited.")
	fs.Int64Var(&cfg.WarnStoreBytes, "warn-store-bytes",
		cfg.WarnStoreBytes,
		"stored bytes at which /health reports the store as warning, which refuses nothing.")
	fs.Int64Var(&cfg.WarnStoreObjects, "warn-store-objects",
		cfg.WarnStoreObjects,
		"stored files at which /health reports the store as warning.")
	fs.DurationVar(&cfg.StoreReconcile, "store-reconcile",
		cfg.StoreReconcile,
		"how often the service walks its stored trees and replaces the running count with the measurement. "+
			"Uploads are counted as they happen, so this walk is the only enumeration the ceiling costs; "+
			"0 measures once at startup.")
	fs.StringVar(&cfg.BlobStore, "blob-store",
		cfg.BlobStore,
		"s3://bucket/prefix that holds the binary, dependency-archive and artifact stores instead of the volume, "+
			"one teams/<team>/ namespace per team. Region and credentials come from the AWS default chain (IRSA on EKS); "+
			"$SPARKWING_S3_ENDPOINT points it at an S3-compatible store. Git mirrors and the registry proxy stay "+
			"on --data-dir. Empty keeps everything on the volume.")
	fs.StringVar(&cfg.ControllerURL, "controller",
		cfg.ControllerURL,
		"controller URL the cache asks, with its token, to count what each team a grant names stores in "+
			"--blob-store and downloads in a UTC day, and where it keeps its egress totals. When the controller cannot "+
			"answer, a team without credits is refused with 503. Required with a grant key and --blob-store.")
	fs.BoolVar(&cfg.DisableProxy, "disable-proxy", cfg.DisableProxy,
		"serve no registry proxy (/proxy/ and /stats). The proxy takes no credential, so a cache reachable "+
			"from outside the cluster sets this.")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr",
		cfg.MetricsAddr,
		"bind address for /metrics and the proxy's /stats. Set it to move both off --addr, and off any ingress "+
			"fronting that listener, onto a port of their own; a cache published outside the cluster sets it with "+
			"--disable-proxy. Empty serves both on --addr.")
	fs.Int64Var(&cfg.ProxyMaxBytes, "proxy-max-bytes",
		cfg.ProxyMaxBytes,
		"size cap for the registry proxy's directory; past it the least recently served entries are evicted, "+
			"which costs one upstream fetch each. 0 leaves the proxy unbounded.")
	fs.IntVar(&cfg.GitForkLimit, "git-fork-limit",
		cfg.GitForkLimit, "max concurrent git subprocesses.")
	readEgress := egress.Bind(fs, nil, egress.ServiceCache, egress.CacheSurfaces)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	creds, err := credentials.Open(*credentialsDir)
	if err != nil {
		return err
	}
	if cfg.APIToken, err = creds.Read(authwire.CacheTokenCredential); err != nil {
		return err
	}
	if cfg.GrantKey, err = creds.Read(authwire.CacheGrantKeyCredential); err != nil {
		return err
	}

	egressCfg, named, err := readEgress()
	if err != nil {
		return err
	}
	cfg.EgressDailyAlarmBytes = egressDailyAlarm(cfg, egressCfg, named)

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
// with a daily alarm unless the operator named one, zero included.
func egressDailyAlarm(cfg cache.Config, egressCfg egress.Config, named egress.Named) int64 {
	if cfg.GrantKey != "" && !named.DailyAlarmBytes {
		return cache.DefaultMultiTeamEgressDailyAlarmBytes
	}
	return egressCfg.GlobalDailyAlarmBytes
}
