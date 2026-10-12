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

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/logexport"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	fsstore "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

const migrateLogsUsage = `usage: sparkwing-controller migrate-logs --root DIR [--archive-store s3://bucket/prefix]
       [--cache-blob-store s3://bucket/prefix] [--default-team default] [--dry-run]

Converts every node log a sparkwing-logs service held into the segments and
manifests this controller reads. It reads the service's storage directly:
--root is the service's volume (its --root) and --archive-store its archive,
if it kept one; the service need not run. The segments go to
--cache-blob-store, the controller's cache bucket, or without one to the
directory beside the controller's state database.

An attempt that already has a manifest is skipped, so the command can be
stopped and run again. --dry-run writes nothing and prints what it would
convert. It exits non-zero naming every run it could not convert.`

func runMigrateLogs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate-logs", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, migrateLogsUsage) }
	root := fs.String("root", "", "the logs service's storage root, as it ran with --root (required)")
	archiveStore := fs.String("archive-store", "", "the logs service's --archive-store, as s3://bucket/prefix; empty when it archived nothing")
	cacheBlobStore := fs.String("cache-blob-store", "",
		"the controller's cache bucket (s3://bucket/prefix); empty writes to the directory beside its state database")
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
