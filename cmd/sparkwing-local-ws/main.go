// Command sparkwing-local-ws is a thin wrapper around
// pkg/localws.Run. Preserved as a standalone binary so a user can
// opt into running the dev server as a separate process (docker
// container, systemd unit, etc.) without the sparkwing CLI in the
// same address space.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/localws"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func main() {
	fs := flag.NewFlagSet("sparkwing-local-ws", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:4343", "bind address")
	home := fs.String("home", "",
		"sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	logStoreURL := fs.String("log-store", "",
		"pluggable log backend URL: fs:///abs/path or s3://bucket/prefix")
	artifactStoreURL := fs.String("artifact-store", "",
		"pluggable artifact backend URL: fs:///abs/path or s3://bucket/prefix")
	readOnly := fs.Bool("read-only", false,
		"reject POST/PUT/DELETE/PATCH on /api/v1/* (auth + webhooks remain open)")
	noLocalStore := fs.Bool("no-local-store", false,
		"skip the local SQLite store; list runs from --artifact-store instead. "+
			"Requires --log-store + --artifact-store. Powers tailing CI runs from a fresh laptop without an ingest step.")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	ctx := context.Background()
	if *noLocalStore && (*logStoreURL == "" || *artifactStoreURL == "") {
		fmt.Fprintln(os.Stderr,
			"sparkwing-local-ws: --no-local-store requires --log-store and --artifact-store")
		os.Exit(1)
	}
	opts := localws.Options{
		Addr:         *addr,
		Home:         *home,
		ReadOnly:     *readOnly,
		NoLocalStore: *noLocalStore,
	}
	if *logStoreURL != "" {
		ls, err := storeurl.OpenLogStore(ctx, *logStoreURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-local-ws: --log-store:", err)
			os.Exit(1)
		}
		opts.LogStore = ls
		opts.LogStoreLabel = schemeOf(*logStoreURL)
	}
	if *artifactStoreURL != "" {
		as, err := storeurl.OpenArtifactStore(ctx, *artifactStoreURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-local-ws: --artifact-store:", err)
			os.Exit(1)
		}
		opts.ArtifactStore = as
		opts.ArtifactStoreLabel = schemeOf(*artifactStoreURL)
	}

	if err := localws.Run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-local-ws:", err)
		os.Exit(1)
	}
}

// schemeOf extracts the scheme from a store URL ("fs", "s3", ...) so
// /api/v1/capabilities can surface it without re-parsing the URL.
func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return raw[:i]
	}
	return "custom"
}
