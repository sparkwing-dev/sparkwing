package main

import (
	"errors"
	"fmt"
	"log/slog"
	neturl "net/url"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// writeProfileBackendsConfig materializes profile.LogStore /
// profile.ArtifactStore into a temp backends.yaml fragment so the
// inner pipeline binary picks them up via the normal backends-
// resolution pathway. Returns ("", noop, nil) when the profile has
// neither field set.
//
// The returned cleanup must be deferred by the caller for the
// lifetime of the child process.
func writeProfileBackendsConfig(logStoreURL, artifactStoreURL string) (path string, cleanup func(), err error) {
	if logStoreURL == "" && artifactStoreURL == "" {
		return "", func() {}, nil
	}
	file := backends.File{}
	if logStoreURL != "" {
		spec, ok := profileStoreURLToSpec(logStoreURL)
		if !ok {
			return "", func() {}, fmt.Errorf("profile log_store %q is not a recognized URL", logStoreURL)
		}
		file.Defaults.Logs = spec
	}
	if artifactStoreURL != "" {
		spec, ok := profileStoreURLToSpec(artifactStoreURL)
		if !ok {
			return "", func() {}, fmt.Errorf("profile artifact_store %q is not a recognized URL", artifactStoreURL)
		}
		file.Defaults.Cache = spec
	}
	body := renderBackendsFile(file)
	tmp, err := os.CreateTemp("", "sparkwing-profile-backends-*.yaml")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", func() {}, err
	}
	cleanup = func() {
		if rerr := os.Remove(tmp.Name()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			slog.Default().Debug("profile backends temp file cleanup", "path", tmp.Name(), "err", rerr)
		}
	}
	return tmp.Name(), cleanup, nil
}

// renderBackendsFile writes a minimal yaml fragment for a synthesized
// File. Only the surfaces actually set are rendered.
func renderBackendsFile(f backends.File) string {
	out := "defaults:\n"
	if f.Defaults.Cache != nil {
		out += "  cache:\n" + renderSpec(*f.Defaults.Cache, "    ")
	}
	if f.Defaults.Logs != nil {
		out += "  logs:\n" + renderSpec(*f.Defaults.Logs, "    ")
	}
	if f.Defaults.State != nil {
		out += "  state:\n" + renderSpec(*f.Defaults.State, "    ")
	}
	return out
}

func renderSpec(s backends.Spec, indent string) string {
	out := indent + "type: " + s.Type + "\n"
	if s.Bucket != "" {
		out += indent + "bucket: " + s.Bucket + "\n"
	}
	if s.Prefix != "" {
		out += indent + "prefix: " + s.Prefix + "\n"
	}
	if s.Path != "" {
		out += indent + "path: " + s.Path + "\n"
	}
	if s.URL != "" {
		out += indent + "url: " + s.URL + "\n"
	}
	if s.URLSource != "" {
		out += indent + "url_source: " + s.URLSource + "\n"
	}
	return out
}

// resolveEffectiveCacheSpec returns the cache backend spec the sparkwing
// CLI should consult before the orchestrator boots: backends.yaml
// defaults plus the matched built-in environment, with no pipeline
// or target context (compile runs before the pipeline-aware
// orchestrator init).
//
// Returns nil when no cache backend is configured. Resolution
// errors are logged at debug level and yield nil so the compile
// loop falls through to the next cache layer instead of failing.
func resolveEffectiveCacheSpec(sparkwingDir string) *backends.Spec {
	file, err := backends.Resolve(sparkwingDir)
	if err != nil {
		slog.Default().Debug("backends.yaml resolve failed", "err", err)
		return nil
	}
	envName, _, _ := backends.DetectEnvironment(file)
	eff := backends.Effective(file, envName, backends.Surfaces{})
	return eff.Cache
}

// profileStoreURLToSpec parses a profile's log_store / artifact_store
// URL into a typed Spec. Supported schemes:
//
//	fs:///abs/path           → backends.Spec{Type: filesystem, Path}
//	s3://bucket/prefix       → backends.Spec{Type: s3, Bucket, Prefix}
//
// Returns (nil, false) on a malformed URL so the caller surfaces a
// clear "profile field X is not a recognized URL" error.
func profileStoreURLToSpec(raw string) (*backends.Spec, bool) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || rest == "" {
		return nil, false
	}
	switch scheme {
	case "fs":
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "~") {
			return nil, false
		}
		if strings.HasPrefix(rest, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, false
			}
			rest = home + rest[1:]
		}
		return &backends.Spec{Type: backends.TypeFilesystem, Path: rest}, true
	case "s3":
		u, err := neturl.Parse("s3://" + rest)
		if err != nil || u.Host == "" {
			return nil, false
		}
		prefix := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
		return &backends.Spec{Type: backends.TypeS3, Bucket: u.Host, Prefix: prefix}, true
	default:
		return nil, false
	}
}
