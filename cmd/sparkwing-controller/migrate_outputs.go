package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const migrateOutputsUsage = `usage: sparkwing-controller migrate-outputs [--cache-blob-store s3://bucket/prefix] [--batch 500]

Moves every node output still held in the nodes table into the output
store: the cache bucket when --cache-blob-store names one, and the output
directory beside the state database otherwise. Outputs of runs that finished
more than 30 days ago move only for each pipeline's newest successful run.

Each output takes a fixed key, and a node that already names an output is
skipped, so the command can be stopped and run again, and run while the
controller serves.`

func runMigrateOutputs(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate-outputs", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, migrateOutputsUsage) }
	cacheBlobStore := fs.String("cache-blob-store", os.Getenv("SPARKWING_CACHE_BLOB_STORE"),
		"the cache bucket the controller serves outputs from (s3://bucket/prefix); empty moves them to the output directory")
	batch := fs.Int("batch", 500, "outputs read per batch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *batch < 1 {
		return fmt.Errorf("--batch must be at least 1")
	}
	ctx := context.Background()
	p, err := paths.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := openControllerStore(ctx, p.StateDB())
	if err != nil {
		return mapStoreOpenError(err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-controller: close state:", cerr)
		}
	}()

	put, provenance, err := outputWriter(ctx, st, p, *cacheBlobStore)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-store.OutputRetention)
	var moved, bytesMoved int64
	afterRun, afterNode := "", ""
	for {
		outputs, err := st.LegacyOutputs(ctx, afterRun, afterNode, *batch, cutoff)
		if err != nil {
			return fmt.Errorf("list inline outputs: %w", err)
		}
		for _, o := range outputs {
			if err := put(ctx, o); err != nil {
				return fmt.Errorf("write output %s/%s: %w", o.RunID, o.NodeID, err)
			}
			if err := st.RecordMigratedOutput(ctx, o, provenance, time.Now()); err != nil {
				return fmt.Errorf("record output %s/%s: %w", o.RunID, o.NodeID, err)
			}
			moved++
			bytesMoved += int64(len(o.Data))
			afterRun, afterNode = o.RunID, o.NodeID
		}
		if len(outputs) > 0 {
			fmt.Fprintf(stdout, "moved %d outputs (%d bytes); last %s/%s\n", moved, bytesMoved, afterRun, afterNode)
		}
		if len(outputs) < *batch {
			break
		}
	}
	fmt.Fprintf(stdout, "done: %d outputs, %d bytes\n", moved, bytesMoved)
	return nil
}

func outputWriter(ctx context.Context, st *store.Store, p paths.Paths, cacheBlobStore string) (func(context.Context, store.LegacyOutput) error, string, error) {
	if strings.HasPrefix(cacheBlobStore, "s3://") {
		cache, err := openTeamStore(ctx, cacheBlobStore, nil)
		if err != nil {
			return nil, "", fmt.Errorf("--cache-blob-store: %w", err)
		}
		return func(ctx context.Context, o store.LegacyOutput) error {
			ref := o.Ref()
			_, err := cache.Put(ctx, string(o.Team), "cloud/"+ref.Key, bytes.NewReader(o.Data), teamblob.PutOptions{
				Size: ref.Size, ContentType: "application/json", Metadata: map[string]string{"sha256": ref.SHA256},
			})
			return err
		}, "cloud", nil
	}
	dir := st.OutputDir()
	if dir == "" {
		dir = p.Root
	}
	return func(_ context.Context, o store.LegacyOutput) error {
		path := store.OutputPath(dir, o.Ref().Key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, o.Data, 0o600)
	}, "local", nil
}
