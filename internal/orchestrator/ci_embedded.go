// ci-embedded mode plumbing: turns SPARKWING_WORKERS into the
// Options.MaxParallel cap.
//
// Storage backend selection (cache + logs) goes through
// ApplyBackendsConfig, which reads .sparkwing/backends.yaml plus
// the built-in environment detect rules (gha, kubernetes). The
// orchestrator surfaces a clear error at run start when ci-embedded
// dispatch can't reach a persistent backend.
package orchestrator

import (
	"fmt"
	"os"
	"strconv"
)

// applyCIEmbeddedEnv populates the worker cap from SPARKWING_WORKERS.
// Cache + logs backend wiring is handled by ApplyBackendsConfig
// (called from RunLocal).
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
	return nil
}
