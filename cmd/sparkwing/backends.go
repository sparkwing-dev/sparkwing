package main

import (
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

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
