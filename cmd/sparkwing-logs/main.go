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

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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

	readEgress := egress.Bind(fs, os.Getenv, egress.ServiceLogs, egress.LogsSurfaces)

	defaults, err := limitsFromEnv(logs.DefaultLimits())
	if err != nil {
		return err
	}
	maxNodeBytes := fs.Int64("max-node-bytes", defaults.MaxNodeBytes,
		"stored-byte cap for one node's log; further appends land a truncation marker instead. "+
			"0 disables the cap (env: SPARKWING_LOGS_MAX_NODE_BYTES)")
	maxRunBytes := fs.Int64("max-run-bytes", defaults.MaxRunBytes,
		"stored-byte cap for all node logs in one run; 0 disables the cap "+
			"(env: SPARKWING_LOGS_MAX_RUN_BYTES)")
	maxInFlightBytes := fs.Int64("max-inflight-bytes", defaults.MaxInFlightBytes,
		"request-body bytes all in-flight appends may hold in memory at once; further appends "+
			"are refused with 503. 0 disables the bound (env: SPARKWING_LOGS_MAX_INFLIGHT_BYTES)")
	minFreeBytes := fs.Int64("min-free-bytes", int64(defaults.MinFreeBytes),
		"free space on the storage volume below which appends are rejected with 507; "+
			"0 disables the floor (env: SPARKWING_LOGS_MIN_FREE_BYTES)")
	retention := fs.Duration("retention", defaults.Retention,
		"how long a run's logs survive after their last write; 0 keeps them forever. The default is "+
			"0, or 720h (30 days) with --archive-store, which a multi-team deployment runs with "+
			"(env: SPARKWING_LOGS_RETENTION)")
	sweepInterval := fs.Duration("sweep-interval", defaults.SweepInterval,
		"how often the retention sweeper runs (env: SPARKWING_LOGS_SWEEP_INTERVAL)")
	searchMaxBytes := fs.Int64("search-max-bytes", defaults.SearchMaxBytes,
		"bytes one search request may read before it returns a truncated result; "+
			"0 disables the cap (env: SPARKWING_LOGS_SEARCH_MAX_BYTES)")
	maxLineBytes := fs.Int64("max-line-bytes", defaults.MaxLineBytes,
		"byte cap for one log line; a longer line is stored cut to the cap with a "+
			"truncation marker in place of its tail. 0 stores a line of any length "+
			"(env: SPARKWING_LOGS_MAX_LINE_BYTES)")
	binaryRatio := fs.Float64("binary-ratio", defaults.BinaryRatio,
		"share of control bytes in one append above which the append reads as binary "+
			"and is dropped, leaving one warning line in the node's log. 0 stores every "+
			"append whatever it holds; 0.3 catches a binary a pipeline cats "+
			"(env: SPARKWING_LOGS_BINARY_RATIO)")
	searchTimeout := fs.Duration("search-timeout", defaults.SearchTimeout,
		"how long one search request may scan before it returns a truncated result; "+
			"0 disables the deadline (env: SPARKWING_LOGS_SEARCH_TIMEOUT)")
	ceilingDefaults, cerr := storeCeilingFromEnv()
	if cerr != nil {
		return cerr
	}
	maxStoreBytes := fs.Int64("max-store-bytes", ceilingDefaults.Limit.MaxBytes,
		"stored bytes across the whole log store at or above which every append is refused with 507 "+
			"naming the ceiling, until a measurement finds the store back under it. 0, the "+
			"default, leaves the store unlimited (env: SPARKWING_LOGS_MAX_STORE_BYTES)")
	maxStoreObjects := fs.Int64("max-store-objects", ceilingDefaults.Limit.MaxObjects,
		"log files across the whole store at or above which every append is refused with 507; "+
			"0 leaves the count unlimited (env: SPARKWING_LOGS_MAX_STORE_OBJECTS)")
	warnStoreBytes := fs.Int64("warn-store-bytes", ceilingDefaults.Limit.WarnBytes,
		"stored bytes at which /api/v1/health reports the store as warning, which refuses "+
			"nothing; 0 disables the warning (env: SPARKWING_LOGS_WARN_STORE_BYTES)")
	warnStoreObjects := fs.Int64("warn-store-objects", ceilingDefaults.Limit.WarnObjects,
		"log files at which /api/v1/health reports the store as warning; 0 disables the "+
			"warning (env: SPARKWING_LOGS_WARN_STORE_OBJECTS)")
	storeReconcile := fs.Duration("store-reconcile", ceilingDefaults.Reconcile,
		"how often the service measures the whole store and replaces its running count with "+
			"the measurement. Appends are counted as they happen, so this walk is the only "+
			"enumeration the ceiling costs; 0 measures once at startup "+
			"(env: SPARKWING_LOGS_STORE_RECONCILE)")
	archiveStore := fs.String("archive-store", os.Getenv("SPARKWING_LOGS_ARCHIVE_STORE"),
		"s3://bucket/prefix that holds finished runs: a run unwritten for --archive-idle is uploaded as one object "+
			"per log file under teams/<team>/runs/<run>/ and leaves the volume, which keeps only live runs; a read "+
			"restores it. --retention then deletes archived runs by day. Region and credentials come from the AWS "+
			"default chain (IRSA on EKS). Empty keeps every run on the volume (env: SPARKWING_LOGS_ARCHIVE_STORE)")
	archiveIdleDefault, err := envDuration("SPARKWING_LOGS_ARCHIVE_IDLE", logs.DefaultArchiveIdle)
	if err != nil {
		return err
	}
	archiveIdle := fs.Duration("archive-idle", archiveIdleDefault,
		"how long a run goes unwritten before it moves to --archive-store (env: SPARKWING_LOGS_ARCHIVE_IDLE)")
	usageReconcileDefault, err := envDuration("SPARKWING_LOGS_USAGE_RECONCILE", 24*time.Hour)
	if err != nil {
		return err
	}
	usageReconcile := fs.Duration("usage-reconcile", usageReconcileDefault,
		"with --archive-store, how often the per-team count of archived logs is replaced by a listing of the "+
			"store. Uploads and deletes keep it between listings and it is saved to the store every five minutes. "+
			"With --controller the count holds teams to their free log share, so every start lists; 0 lists only "+
			"at such a start or when no saved count exists (env: SPARKWING_LOGS_USAGE_RECONCILE)")
	_ = fs.Parse(args)
	*retention = archiveRetention(*retention, fs.Changed("retention") || os.Getenv("SPARKWING_LOGS_RETENTION") != "", *archiveStore)

	if err := checkNonNegative(
		flagValue{"--max-node-bytes", *maxNodeBytes},
		flagValue{"--max-run-bytes", *maxRunBytes},
		flagValue{"--max-inflight-bytes", *maxInFlightBytes},
		flagValue{"--min-free-bytes", *minFreeBytes},
		flagValue{"--search-max-bytes", *searchMaxBytes},
		flagValue{"--max-line-bytes", *maxLineBytes},
		flagValue{"--max-store-bytes", *maxStoreBytes},
		flagValue{"--max-store-objects", *maxStoreObjects},
		flagValue{"--warn-store-bytes", *warnStoreBytes},
		flagValue{"--warn-store-objects", *warnStoreObjects},
		flagValue{"--store-reconcile", int64(*storeReconcile)},
		flagValue{"--retention", int64(*retention)},
		flagValue{"--sweep-interval", int64(*sweepInterval)},
		flagValue{"--search-timeout", int64(*searchTimeout)},
		flagValue{"--archive-idle", int64(*archiveIdle)},
		flagValue{"--usage-reconcile", int64(*usageReconcile)},
	); err != nil {
		return err
	}
	if *binaryRatio < 0 || *binaryRatio > 1 {
		return fmt.Errorf("--binary-ratio must be between 0 and 1; pass 0 to store every append")
	}
	// safety: a cap under the marker could not store a cut line and its marker inside
	// the cap, so the bound the operator asked for would not hold.
	if *maxLineBytes > 0 && *maxLineBytes < logs.MinLineBytes {
		return fmt.Errorf("--max-line-bytes must be at least %d, the size of the marker a cut line carries "+
			"plus a byte of output; pass 0 to store a line of any length", logs.MinLineBytes)
	}
	ceiling := objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{
			MaxBytes:    *maxStoreBytes,
			MaxObjects:  *maxStoreObjects,
			WarnBytes:   *warnStoreBytes,
			WarnObjects: *warnStoreObjects,
		},
		Reconcile: *storeReconcile,
	}
	limits := logs.Limits{
		MaxNodeBytes:     *maxNodeBytes,
		MaxRunBytes:      *maxRunBytes,
		MaxInFlightBytes: *maxInFlightBytes,
		MinFreeBytes:     uint64(*minFreeBytes),
		Retention:        *retention,
		SweepInterval:    *sweepInterval,
		SearchMaxBytes:   *searchMaxBytes,
		SearchTimeout:    *searchTimeout,
		MaxLineBytes:     *maxLineBytes,
		BinaryRatio:      *binaryRatio,
	}

	egressCfg, _, err := readEgress()
	if err != nil {
		return err
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
	var archive *logs.ArchiveOptions
	if *archiveStore != "" {
		store, err := openArchive(ctx, *archiveStore, *controllerURL != "")
		if err != nil {
			return fmt.Errorf("--archive-store: %w", err)
		}
		archive = &logs.ArchiveOptions{Store: store, Idle: *archiveIdle, UsageReconcile: *usageReconcile}
	}
	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-logs"})
	defer func() { _ = tel.Shutdown(context.Background()) }()
	return logs.ServeWith(ctx, logs.ServeOptions{
		Root:          *root,
		Addr:          *addr,
		ControllerURL: *controllerURL,
		Private:       privateRoot,
		Limits:        &limits,
		StoreCeiling:  ceiling,
		Egress:        egress.New(egressCfg),
		Archive:       archive,
	})
}

// safety: an archive holds every team's logs past the volume, so a service
// with one prunes them after 30 days unless the operator named a retention,
// zero included; a free team's log share is then a window, not a lifetime.
func archiveRetention(retention time.Duration, named bool, archiveStore string) time.Duration {
	if archiveStore == "" || named {
		return retention
	}
	return logs.DefaultArchiveRetention
}

type flagValue struct {
	name string
	n    int64
}

// safety: a negative bound would silently disable the cap it names, so it stops the service instead.
func checkNonNegative(values ...flagValue) error {
	for _, v := range values {
		if v.n < 0 {
			return fmt.Errorf("%s must not be negative; pass 0 to turn that bound off", v.name)
		}
	}
	return nil
}

// safety: a value the operator meant as a bound must not decay into the default without saying so.
func limitsFromEnv(def logs.Limits) (logs.Limits, error) {
	var err error
	if def.MaxNodeBytes, err = envInt64("SPARKWING_LOGS_MAX_NODE_BYTES", def.MaxNodeBytes); err != nil {
		return def, err
	}
	if def.MaxRunBytes, err = envInt64("SPARKWING_LOGS_MAX_RUN_BYTES", def.MaxRunBytes); err != nil {
		return def, err
	}
	if def.MaxInFlightBytes, err = envInt64("SPARKWING_LOGS_MAX_INFLIGHT_BYTES", def.MaxInFlightBytes); err != nil {
		return def, err
	}
	minFree, err := envInt64("SPARKWING_LOGS_MIN_FREE_BYTES", int64(def.MinFreeBytes))
	if err != nil {
		return def, err
	}
	def.MinFreeBytes = uint64(minFree)
	if def.Retention, err = envDuration("SPARKWING_LOGS_RETENTION", def.Retention); err != nil {
		return def, err
	}
	if def.SweepInterval, err = envDuration("SPARKWING_LOGS_SWEEP_INTERVAL", def.SweepInterval); err != nil {
		return def, err
	}
	if def.SearchMaxBytes, err = envInt64("SPARKWING_LOGS_SEARCH_MAX_BYTES", def.SearchMaxBytes); err != nil {
		return def, err
	}
	if def.MaxLineBytes, err = envInt64("SPARKWING_LOGS_MAX_LINE_BYTES", def.MaxLineBytes); err != nil {
		return def, err
	}
	if def.BinaryRatio, err = envRatio("SPARKWING_LOGS_BINARY_RATIO", def.BinaryRatio); err != nil {
		return def, err
	}
	def.SearchTimeout, err = envDuration("SPARKWING_LOGS_SEARCH_TIMEOUT", def.SearchTimeout)
	return def, err
}

func envInt64(name string, def int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a byte count", name, raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s=%q must not be negative; pass 0 to turn that bound off", name, raw)
	}
	return n, nil
}

// safety: a value the operator meant as a ceiling must not decay into "unlimited" without saying so.
func storeCeilingFromEnv() (objectguard.CeilingConfig, error) {
	var cfg objectguard.CeilingConfig
	var err error
	if cfg.Limit.MaxBytes, err = envInt64("SPARKWING_LOGS_MAX_STORE_BYTES", 0); err != nil {
		return cfg, err
	}
	if cfg.Limit.MaxObjects, err = envInt64("SPARKWING_LOGS_MAX_STORE_OBJECTS", 0); err != nil {
		return cfg, err
	}
	if cfg.Limit.WarnBytes, err = envInt64("SPARKWING_LOGS_WARN_STORE_BYTES", 0); err != nil {
		return cfg, err
	}
	if cfg.Limit.WarnObjects, err = envInt64("SPARKWING_LOGS_WARN_STORE_OBJECTS", 0); err != nil {
		return cfg, err
	}
	cfg.Reconcile, err = envDuration("SPARKWING_LOGS_STORE_RECONCILE", objectguard.DefaultCeilingReconcile)
	return cfg, err
}

func envRatio(name string, def float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 || f > 1 {
		return 0, fmt.Errorf("%s=%q is not a share between 0 and 1", name, raw)
	}
	return f, nil
}

func envDuration(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration such as 168h or 30m", name, raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s=%q must not be negative; pass 0 to turn that bound off", name, raw)
	}
	return d, nil
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

// safety: a logs service that authenticates teams holds each to its log
// share over the archive's count, so it lists the archive at start rather
// than trust a saved count a crash left behind.
func openArchive(ctx context.Context, raw string, reconcileAtStart bool) (*teamblob.Store, error) {
	client, bucket, prefix, err := storeurl.OpenS3(ctx, raw)
	if err != nil {
		return nil, err
	}
	return teamblob.New(teamblob.Options{Bucket: bucket, Prefix: prefix, Client: client, ReconcileAtStart: reconcileAtStart})
}
