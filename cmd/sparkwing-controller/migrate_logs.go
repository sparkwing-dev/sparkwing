package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/logexport"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const migrateLogsUsage = `usage: sparkwing-controller migrate-logs --root DIR [--archive-store s3://bucket/prefix]
       [--cache-blob-store s3://bucket/prefix] [--credentials-dir DIR] [--default-team default] [--dry-run]

Converts every node log a sparkwing-logs service held into the segments and
manifests this controller reads, and records each in the controller's state
database so the controller prunes and deletes it with its run. It reads the
service's storage directly: --root is the service's volume (its --root) and
--archive-store its archive, if it kept one; the service need not run. The
segments go to --cache-blob-store, the controller's cache bucket, or without
one to the directory beside the controller's state database.

A run the volume holds only part of takes the rest from the archive. A run
that cannot be converted whole -- one the controller's database does not
hold, whose metadata cannot be read, whose file is missing, or whose node a
log file's name cannot be told apart from another -- is not converted and is
named. An attempt that already has a manifest is skipped, so the command can
be stopped and run again. --dry-run writes nothing, leaves the state database
unopened, and prints what it would convert; since it checks no run against
the database, the real run may still name runs it cannot convert. It exits
non-zero naming every run it could not convert.

Stop the controller first: opening its state database upgrades the schema,
so an older controller cannot resume afterward.`

func runMigrateLogs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate-logs", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, migrateLogsUsage) }
	root := fs.String("root", "", "the logs service's storage root, as it ran with --root (required)")
	archiveStore := fs.String("archive-store", "", "the logs service's --archive-store, as s3://bucket/prefix; empty when it archived nothing")
	cacheBlobStore := fs.String("cache-blob-store", "",
		"the controller's cache bucket (s3://bucket/prefix); empty writes to the directory beside its state database")
	credentialsDir := fs.String(credentials.FlagName, "",
		"the controller's credentials directory; its "+credPGURL+" file selects the PostgreSQL store")
	defaultTeam := fs.String("default-team", "default", "the team of a run the logs service recorded no team for")
	dryRun := fs.Bool("dry-run", false, "count what would be converted and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("migrate-logs needs --root, the logs service's storage root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opts := logexport.ExportOptions{Root: *root, DefaultTeam: *defaultTeam, DryRun: *dryRun}
	// safety: opening the state database upgrades its schema, which a dry run must not do to a controller
	// the operator may still return to.
	if !*dryRun {
		p, err := paths.DefaultPaths()
		if err != nil {
			return err
		}
		creds, err := readCredentials(*credentialsDir)
		if err != nil {
			return err
		}
		st, err := openControllerStore(ctx, p.StateDB(), creds.PGURL)
		if err != nil {
			return mapStoreOpenError(err)
		}
		defer func() {
			if cerr := st.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, "sparkwing-controller: close state:", cerr)
			}
		}()
		opts.Runs = storeRuns{st}
	}
	if *archiveStore != "" {
		archive, err := openTeamStore(ctx, *archiveStore, nil)
		if err != nil {
			return fmt.Errorf("--archive-store: %w", err)
		}
		opts.Archive = archive
	}
	to, err := migrateLogsDestination(ctx, *cacheBlobStore)
	if err != nil {
		return err
	}
	opts.To = to
	report, err := logexport.Export(ctx, opts)
	verb := "converted"
	if *dryRun {
		verb = "would convert"
	}
	fmt.Fprintf(stdout, "%s %d run(s), %d attempt(s), %d byte(s); %d attempt(s) already converted\n",
		verb, report.Runs, report.Attempts, report.Bytes, report.Skipped)
	failed := make([]string, 0, len(report.Failed))
	for runID := range report.Failed {
		failed = append(failed, runID)
	}
	sort.Strings(failed)
	for _, runID := range failed {
		fmt.Fprintf(stdout, "run %s: %v\n", runID, report.Failed[runID])
	}
	return err
}

type storeRuns struct{ st *store.Store }

func (r storeRuns) RunNodes(ctx context.Context, team, runID string) (time.Time, []string, error) {
	tn, err := r.st.ForTeam(ctx, store.Team(team))
	if err != nil {
		return time.Time{}, nil, err
	}
	run, err := tn.GetRun(ctx, runID)
	if err != nil {
		return time.Time{}, nil, err
	}
	nodes, err := tn.ListNodes(ctx, runID)
	if err != nil {
		return time.Time{}, nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	at := run.StartedAt
	if run.FinishedAt != nil {
		at = *run.FinishedAt
	}
	return at, ids, nil
}

func (r storeRuns) RecordLog(ctx context.Context, l store.NodeLog, at time.Time) error {
	rec, err := r.st.OpenNodeLog(ctx, l, at)
	if err != nil {
		return err
	}
	return r.st.MarkNodeLogSealed(ctx, rec, l.StoredBytes, at)
}

func migrateLogsDestination(ctx context.Context, cacheBlobStore string) (storage.ArtifactStore, error) {
	if strings.HasPrefix(cacheBlobStore, "s3://") {
		client, bucket, prefix, err := s3store.Open(ctx, cacheBlobStore)
		if err != nil {
			return nil, fmt.Errorf("--cache-blob-store: %w", err)
		}
		return s3store.NewArtifactStore(bucket, prefix, client), nil
	}
	if cacheBlobStore != "" {
		return nil, fmt.Errorf("--cache-blob-store %q is not an s3:// URL", cacheBlobStore)
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return fsstore.NewArtifactStore(filepath.Dir(p.StateDB()))
}
