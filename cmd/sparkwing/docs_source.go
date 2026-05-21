// Helpers shared between `sparkwing docs` and `sparkwing docs
// migrations` for the --web / --version / --no-cache flags. The CLI
// stays hermetic by default; --web opts into network fetches against
// sparkwing.dev with an on-disk cache.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// docsWebFlags is the bundle of network-related flags added to every
// docs verb. Callers register the flags on their own FlagSet via
// register(), then call resolve() after parsing.
type docsWebFlags struct {
	web     bool
	version string
	noCache bool
}

// registerWebFlags attaches --web, --version, and --no-cache to fs.
// includeVersion lets `all` / `search` skip the --version flag since
// they don't take a version argument.
func registerWebFlags(fs *flag.FlagSet, f *docsWebFlags, includeVersion bool) {
	fs.BoolVar(&f.web, "web", false, "fetch from sparkwing.dev instead of the embedded corpus")
	if includeVersion {
		fs.StringVar(&f.version, "version", "", "doc version: vX.Y.Z, 'latest', or empty for this CLI's embedded version")
	}
	fs.BoolVar(&f.noCache, "no-cache", false, "with --web, bypass the on-disk cache (read + write)")
}

// embeddedVersion returns this CLI's own version, or "" if no
// release version is baked in.
func embeddedVersion() string {
	v := installedVersion()
	if semver.IsValid(v) {
		return v
	}
	return ""
}

// resolveDocSource decides where to read a doc / list / migration
// from, given the parsed flags. Returns a webResolution describing
// the chosen source (embed vs web) and, when web, a configured
// WebClient. Errors carry the clean user-facing messages from the
// task spec.
type webResolution struct {
	useWeb     bool
	version    string // canonical: "vX.Y.Z" or "latest"; "" means embed-only
	client     *docs.WebClient
	versions   *docs.Versions // nil unless --web AND discovery succeeded
	discoveryW string         // warning printed when discovery failed
}

// resolveSource validates the requested version against the embed
// (when --web is off) or against sparkwing.dev/versions.json (when
// --web is on). The returned context.Context carries the call timeout
// for any subsequent web fetches.
func resolveSource(ctx context.Context, f docsWebFlags) (webResolution, error) {
	r := webResolution{useWeb: f.web, version: strings.TrimSpace(f.version)}
	embedVer := embeddedVersion()

	if !r.useWeb {
		if r.version == "" || r.version == embedVer || r.version == docs.LatestAlias {
			return r, nil
		}
		if !semver.IsValid(r.version) {
			return r, fmt.Errorf("--version %q is not a valid semver (e.g. v0.4.0)", r.version)
		}
		return r, fmt.Errorf(
			"version %s not in this binary's embed (this CLI is %s). "+
				"Rerun with --web to fetch from sparkwing.dev, or install the "+
				"matching CLI: sparkwing version update --cli --version %s",
			r.version, displayEmbedded(embedVer), r.version)
	}

	r.client = docs.NewWebClient()
	r.client.NoCache = f.noCache

	v, err := r.client.Versions(ctx)
	if err != nil {
		// Offline-fallback path. Per the spec, log a one-line warning
		// to stderr and continue; per-resource fetch may also fail
		// with a more specific message.
		r.discoveryW = fmt.Sprintf(
			"unable to reach %s/versions.json (%v). Proceeding without version validation; the per-resource fetch may also fail. Use --no-cache to bypass any stale cache.",
			r.client.BaseURL, err)
	} else {
		r.versions = &v
	}

	if r.version == "" {
		r.version = embedVer
	}
	if r.version == "" {
		// No embedded version; pick latest if we have it.
		if r.versions != nil && r.versions.Latest != "" {
			r.version = r.versions.Latest
		} else {
			r.version = docs.LatestAlias
		}
	}

	if r.version != docs.LatestAlias && !semver.IsValid(r.version) {
		return r, fmt.Errorf("--version %q is not a valid semver (e.g. v0.4.0) or 'latest'", r.version)
	}

	if r.versions != nil && r.version != docs.LatestAlias {
		if !containsVersion(r.versions.Versions, r.version) {
			return r, fmt.Errorf(
				"version %s not available on %s. Available: %s (see %s/versions.json)",
				r.version, r.client.BaseURL, strings.Join(r.versions.Versions, ", "), r.client.BaseURL)
		}
	}
	return r, nil
}

func containsVersion(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func displayEmbedded(v string) string {
	if v == "" {
		return "a local / dev build"
	}
	return v
}

// printDiscoveryWarning writes the resolution's discovery warning, if
// any, to stderr. Idempotent — callers can invoke once they know the
// resolution went through.
func printDiscoveryWarning(r webResolution) {
	if r.discoveryW != "" {
		fmt.Fprintf(os.Stderr, "%s %s\n", color.Yellow("warning:"), r.discoveryW)
	}
}

// newWebContext returns a context with a 30s timeout suitable for a
// single CLI invocation: long enough to cover one retry + cache write
// on a slow link, short enough that a hung connection doesn't hang
// the user indefinitely.
func newWebContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// fetchDocWeb fetches one doc from the resolution's source, returning
// the raw markdown. Wraps 404 into a clean user-facing error per the
// task spec.
func fetchDocWeb(ctx context.Context, r webResolution, slug string) (string, error) {
	body, err := r.client.Doc(ctx, r.version, slug)
	if err == nil {
		return body, nil
	}
	if errors.Is(err, docs.ErrNotFound) {
		return "", fmt.Errorf(
			"topic %q not found at %s (%s/docs/%s/%s.md returned 404). Try `sparkwing docs versions --web` to confirm the version exists.",
			slug, displayVersion(r.version), r.client.BaseURL, r.version, slug)
	}
	return "", err
}

// fetchMigrationWeb fetches one migration guide from the resolution's
// source. Migrations require an explicit semver; LatestAlias is
// rejected upstream.
func fetchMigrationWeb(ctx context.Context, r webResolution) (string, error) {
	if r.version == "" || r.version == docs.LatestAlias {
		return "", errors.New("docs migrations: --version must be a specific semver (e.g. --version v0.4.0); 'latest' is not meaningful for migrations")
	}
	body, err := r.client.Migration(ctx, r.version)
	if err == nil {
		return body, nil
	}
	if errors.Is(err, docs.ErrNotFound) {
		return "", fmt.Errorf(
			"migration guide for %s not found (%s/migrations/%s.md returned 404). Try `sparkwing docs versions --web` to confirm the version exists.",
			r.version, r.client.BaseURL, r.version)
	}
	return "", err
}

func displayVersion(v string) string {
	if v == "" || v == docs.LatestAlias {
		return "latest"
	}
	return v
}
