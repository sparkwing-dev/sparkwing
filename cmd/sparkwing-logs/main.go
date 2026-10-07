package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	fs := flag.NewFlagSet("sparkwing-logs", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:4345", "bind address")
	root := fs.String("root", "", "storage root; explicit roots keep shared/PVC creation modes subject to umask (default: private $SPARKWING_HOME/logs-service)")
	controllerURL := fs.String("controller", "",
		"controller URL used to resolve sw*_ tokens via /api/v1/auth/whoami; empty disables auth")
	requireAuth := fs.Bool("require-auth", false,
		"refuse to start unless --controller is an absolute http(s) URL, "+
			"guarding against accidentally deploying a logs service that serves, forges, and "+
			"deletes every run's logs for anyone who can reach it. Leave unset "+
			"for laptop-local use.")

	readEgress := egress.Bind(fs, nil, egress.ServiceLogs, egress.LogsSurfaces)

	defaults := logs.DefaultLimits()
	maxNodeBytes := fs.Int64("max-node-bytes", defaults.MaxNodeBytes,
		"stored-byte cap for one node's log; further appends land a truncation marker instead. "+
			"0 disables the cap")
	maxRunBytes := fs.Int64("max-run-bytes", defaults.MaxRunBytes,
		"stored-byte cap for all node logs in one run; 0 disables the cap")
	maxInFlightBytes := fs.Int64("max-inflight-bytes", defaults.MaxInFlightBytes,
		"request-body bytes all in-flight appends may hold in memory at once; further appends "+
			"are refused with 503. 0 disables the bound")
	minFreeBytes := fs.Int64("min-free-bytes", int64(defaults.MinFreeBytes),
		"free space on the storage volume below which appends are rejected with 507; "+
			"0 disables the floor")
	retention := fs.Duration("retention", defaults.Retention,
		"how long a run's logs survive after their last write; 0 keeps them forever. The default is "+
			"0, or 2160h (90 days) with --archive-store, which a multi-team deployment runs with. A run of a "+
			"team without credits keeps its archived logs 720h (30 days) at most")
	sweepInterval := fs.Duration("sweep-interval", defaults.SweepInterval,
		"how often the retention sweeper runs")
	searchMaxBytes := fs.Int64("search-max-bytes", defaults.SearchMaxBytes,
		"bytes one search request may read before it returns a truncated result; "+
			"0 disables the cap")
	maxLineBytes := fs.Int64("max-line-bytes", defaults.MaxLineBytes,
		"byte cap for one log line; a longer line is stored cut to the cap with a "+
			"truncation marker in place of its tail. 0 stores a line of any length")
	binaryRatio := fs.Float64("binary-ratio", defaults.BinaryRatio,
		"share of control bytes in one append above which the append reads as binary "+
			"and is dropped, leaving one warning line in the node's log. 0 stores every "+
			"append whatever it holds; 0.3 catches a binary a pipeline cats")
	searchTimeout := fs.Duration("search-timeout", defaults.SearchTimeout,
		"how long one search request may scan before it returns a truncated result; "+
			"0 disables the deadline")
	maxStoreBytes := fs.Int64("max-store-bytes", 0,
		"stored bytes across the whole log store at or above which every append is refused with 507 "+
			"naming the ceiling, until a measurement finds the store back under it. 0, the "+
			"default, leaves the store unlimited")
	maxStoreObjects := fs.Int64("max-store-objects", 0,
		"log files across the whole store at or above which every append is refused with 507; "+
			"0 leaves the count unlimited")
	warnStoreBytes := fs.Int64("warn-store-bytes", 0,
		"stored bytes at which /api/v1/health reports the store as warning, which refuses "+
			"nothing; 0 disables the warning")
	warnStoreObjects := fs.Int64("warn-store-objects", 0,
		"log files at which /api/v1/health reports the store as warning; 0 disables the "+
			"warning")
	storeReconcile := fs.Duration("store-reconcile", objectguard.DefaultCeilingReconcile,
		"how often the service measures the whole store and replaces its running count with "+
			"the measurement. Appends are counted as they happen, so this walk is the only "+
			"enumeration the ceiling costs; 0 measures once at startup")
	archiveStore := fs.String("archive-store", "",
		"s3://bucket/prefix that holds finished runs: a run unwritten for --archive-idle is uploaded as one object "+
			"per log file under teams/<team>/runs/<run>/ and leaves the volume, which keeps only live runs; a read "+
			"restores it. --retention then deletes archived runs by day. Region and credentials come from the AWS "+
			"default chain (IRSA on EKS). Empty keeps every run on the volume")
	archiveIdle := fs.Duration("archive-idle", logs.DefaultArchiveIdle,
		"how long a run goes unwritten before it moves to --archive-store")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	*retention = archiveRetention(*retention, fs.Changed("retention"), *archiveStore)

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
		store, err := openArchive(ctx, *archiveStore)
		if err != nil {
			return fmt.Errorf("--archive-store: %w", err)
		}
		archive = &logs.ArchiveOptions{Store: store, Idle: *archiveIdle}
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
// with one prunes them after 90 days unless the operator named a retention,
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

// safety: --require-auth advertises auth on /api/v1/health, so a URL that resolves no token must not start.
func checkControllerURL(raw string) error {
	const remedy = "; point the logs service at a controller so it can " +
		"resolve caller tokens, or drop --require-auth for laptop-local use"
	if strings.TrimSpace(raw) == "" {
		return errors.New("--require-auth is set but " +
			"--controller is empty" + remedy)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("--require-auth is set but "+
			"--controller %q is not a URL: %w"+remedy, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--require-auth is set but "+
			"--controller %q is not an absolute http(s) URL"+remedy, raw)
	}
	return nil
}

func openArchive(ctx context.Context, raw string) (*teamblob.Store, error) {
	client, bucket, prefix, err := storeurl.OpenS3(ctx, raw)
	if err != nil {
		return nil, err
	}
	return teamblob.New(teamblob.Options{Bucket: bucket, Prefix: prefix, Client: client})
}
