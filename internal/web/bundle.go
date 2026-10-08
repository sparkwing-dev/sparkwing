package web

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
)

//go:embed all:next-out
var nextBundle embed.FS

func VerifyBundleEmbedded() error {
	if bundleMissingReason(nextBundle) != "" {
		return errors.New(missingBundleMessage)
	}
	return nil
}

// VerifyBundle reports whether bundleFS carries a dashboard to serve. The
// bundle is rooted at its index.html, the way BundleFS returns it.
func VerifyBundle(bundleFS fs.FS) error {
	if _, err := fs.Stat(bundleFS, "index.html"); err != nil {
		return fmt.Errorf("dashboard bundle carries no index.html: %w", err)
	}
	return nil
}

func bundleMissingReason(bundle fs.FS) string {
	if _, err := fs.Stat(bundle, "next-out/index.html"); err != nil {
		return "dashboard bundle not built in this checkout; run: bash bin/build-web.sh"
	}
	return ""
}

const missingBundleMessage = `dashboard assets are missing from this binary.

The Next.js dashboard bundle is a generated artifact and is not checked
into the sparkwing repository, so "go install" or a source build without
running bin/build-web.sh first produces a binary that compiles cleanly
but serves a silent 404 on every dashboard page.

To run the dashboard locally, install the sparkwing release binary and
use the serve subcommand -- not "go install":

  curl -L -o sparkwing \
    https://github.com/sparkwing-dev/sparkwing/releases/latest/download/sparkwing-linux-amd64
  chmod +x sparkwing && sudo mv sparkwing /usr/local/bin/sparkwing
  sparkwing serve start

Release binaries for every platform are listed at:

  https://github.com/sparkwing-dev/sparkwing/releases/latest

To serve the dashboard from a cluster controller, use the container image
rather than a source build -- it already has the dashboard bundle baked in.

If you are building from a sparkwing checkout, generate the dashboard
bundle first, then reinstall:

  bash bin/build-web.sh
  go install ./cmd/sparkwing`

// BundleFS returns the embedded dashboard bundle rooted at its index.html,
// the shape [Pages] serves.
func BundleFS() fs.FS {
	subFS, err := fs.Sub(nextBundle, "next-out")
	if err != nil {
		panic(fmt.Sprintf("web: embed fs.Sub failed: %v", err)) //nolint:forbidigo // unreachable post-VerifyBundleEmbedded; build-time invariant
	}
	return subFS
}
