// ci-embedded mode plumbing: turns SPARKWING_WORKERS into the
// Options.MaxParallel cap, and surfaces a one-shot warning when
// ci-embedded mode runs with no cache/logs backend configured
// (artifacts will not survive the CI VM).
//
// Storage backend selection (cache + logs) goes through
// ApplyBackendsConfig, which honors .sparkwing/backends.yaml plus
// the legacy SPARKWING_*_STORE env vars via the deprecation shim.
package orchestrator

import (
	"fmt"
	"os"
	"strconv"
)

// applyCIEmbeddedEnv populates the worker cap from
// SPARKWING_WORKERS and warns when ci-embedded mode lacks a
// persistent backend configuration. Cache + logs backend wiring is
// handled by ApplyBackendsConfig (called from RunLocal).
func applyCIEmbeddedEnv(opts *Options) error {
	if w := os.Getenv("SPARKWING_WORKERS"); w != "" {
		n, err := strconv.Atoi(w)
		if err != nil || n < 0 {
			return fmt.Errorf("SPARKWING_WORKERS=%q: must be a non-negative integer", w)
		}
		if n > 0 {
			opts.MaxParallel = n
		}
	}

	if os.Getenv("SPARKWING_MODE") == "ci-embedded" &&
		os.Getenv("SPARKWING_LOG_STORE") == "" &&
		os.Getenv("SPARKWING_ARTIFACT_STORE") == "" {
		fmt.Fprintln(os.Stderr,
			"warn: --mode=ci-embedded with no cache or logs backend configured; "+
				"logs + artifacts will live in this VM's filesystem and not survive job exit. "+
				"Declare them in .sparkwing/backends.yaml.")
	}
	return nil
}
