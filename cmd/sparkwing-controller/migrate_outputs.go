package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
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

Each output takes a fixed key, and a node that already names another
output is skipped, so the command can be stopped and run again. It repeats
whole passes until one moves nothing, so it can run while the old
controller serves; an output written inline after the last pass is not
moved, so the cutover runs it once more with the old controller stopped.`

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
	moved, err := st.MoveLegacyOutputs(ctx, time.Now().Add(-store.OutputRetention), *batch, provenance, false, put)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "done: %d outputs, %d bytes\n", moved.Outputs, moved.Bytes)
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
		return store.WriteOutputFileDurably(store.OutputPath(dir, o.Ref().Key), o.Data)
	}, "local", nil
}
