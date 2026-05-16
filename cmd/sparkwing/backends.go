package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// writeProfileBackendsConfig materializes profile.LogStore /
// profile.ArtifactStore into a temp backends.yaml fragment so the
// inner pipeline binary picks them up via the normal backends-
// resolution pathway instead of through the deprecated
// SPARKWING_*_STORE env-var shim. Returns ("", noop, nil) when the
// profile has neither field set.
//
// The returned cleanup must be deferred by the caller for the
// lifetime of the child process.
func writeProfileBackendsConfig(logStoreURL, artifactStoreURL string) (path string, cleanup func(), err error) {
	if logStoreURL == "" && artifactStoreURL == "" {
		return "", func() {}, nil
	}
	file := backends.File{}
	if logStoreURL != "" {
		spec, ok := backends.LegacyURLToSpec(logStoreURL)
		if !ok {
			return "", func() {}, fmt.Errorf("profile log_store %q is not a recognized URL", logStoreURL)
		}
		file.Defaults.Logs = spec
	}
	if artifactStoreURL != "" {
		spec, ok := backends.LegacyURLToSpec(artifactStoreURL)
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
		tmp.Close()
		os.Remove(tmp.Name())
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
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

// resolveEffectiveCacheSpec returns the cache backend spec the wing
// CLI should consult before the orchestrator boots: file defaults +
// built-in environments + the SPARKWING_*_STORE shim, with no
// pipeline/target context (compile runs before the
// pipeline-aware orchestrator init).
//
// Returns nil when no cache backend is configured. Resolution
// errors are logged at debug level and yield nil so the compile
// loop falls through to the next cache layer instead of failing.
func resolveEffectiveCacheSpec(sparkwingDir string) *backends.Spec {
	file, err := backends.ResolveWithEnv(sparkwingDir)
	if err != nil {
		slog.Default().Debug("backends.yaml resolve failed", "err", err)
		return nil
	}
	envName, _, _ := backends.DetectEnvironment(file)
	eff := backends.Effective(file, envName, backends.Surfaces{})
	return eff.Cache
}
