// ci-embedded mode plumbing: turns SPARKWING_WORKERS into the
// Options.MaxParallel cap.
//
// Storage backend selection (state + cache + logs) comes from the
// resolved storage profile (state/cache/logs surfaces, or a detect:
// block that auto-selects a CI profile). The orchestrator surfaces a
// clear error at run start when ci-embedded dispatch can't reach a
// persistent backend.
package orchestrator

import (
	"fmt"
	"os"
	"strconv"
)

// applyCIEmbeddedEnv populates the worker cap from SPARKWING_WORKERS.
// Cache + logs backend wiring comes from the resolved profile
// (ApplyProfileBackends, called from RunLocal).
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
