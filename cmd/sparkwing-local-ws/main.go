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
	"github.com/sparkwing-dev/sparkwing/profile"
)

func main() {
	fs := flag.NewFlagSet("sparkwing-local-ws", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:4343", "bind address")
	home := fs.String("home", "",
		"sparkwing state directory (default: $SPARKWING_HOME or ~/.sparkwing)")
	on := fs.String("on", "",
		"profile name from ~/.config/sparkwing/profiles.yaml; "+
			"uses its log_store + artifact_store fields")
	logStoreURL := fs.String("log-store", "",
		"pluggable log backend URL: fs:///abs/path or s3://bucket/prefix. "+
			"Overrides --on. Intended for ci-embedded VMs without a profiles.yaml.")
	artifactStoreURL := fs.String("artifact-store", "",
		"pluggable artifact backend URL: fs:///abs/path or s3://bucket/prefix. "+
			"Overrides --on. Intended for ci-embedded VMs without a profiles.yaml.")
	readOnly := fs.Bool("read-only", false,
		"reject POST/PUT/DELETE/PATCH on /api/v1/* (auth + webhooks remain open)")
	noLocalStore := fs.Bool("no-local-store", false,
		"skip the local SQLite store; list runs from --artifact-store instead. "+
			"Requires --log-store + --artifact-store. Powers tailing CI runs from a fresh laptop without an ingest step.")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	if *on != "" {
		path, err := profile.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-local-ws: profiles path:", err)
			os.Exit(1)
		}
		cfg, err := profile.Load(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-local-ws: profiles load:", err)
			os.Exit(1)
		}
		prof, err := profile.Resolve(cfg, *on)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sparkwing-local-ws: --on:", err)
			os.Exit(1)
		}
		if *logStoreURL == "" {
			*logStoreURL = prof.LogStore
		}
		if *artifactStoreURL == "" {
			*artifactStoreURL = prof.ArtifactStore
		}
	}

	ctx := context.Background()
	if *noLocalStore && (*logStoreURL == "" || *artifactStoreURL == "") {
		fmt.Fprintln(os.Stderr,
			"sparkwing-local-ws: --no-local-store requires --log-store and --artifact-store (or an --on profile that supplies them)")
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
