// ci-embedded mode plumbing: turns env vars set by the wing CLI
// (SPARKWING_MODE, SPARKWING_WORKERS, SPARKWING_{LOG,ARTIFACT}_STORE)
// into Options fields. Storage URLs are honored outside ci-embedded
// mode too.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// applyCIEmbeddedEnv populates opts from SPARKWING_* env vars. Warns
// when ci-embedded mode runs without configured storage (artifacts
// + logs won't survive the CI VM).
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

	logStoreURL := os.Getenv("SPARKWING_LOG_STORE")
	artifactStoreURL := os.Getenv("SPARKWING_ARTIFACT_STORE")

	if logStoreURL != "" {
		ls, err := storeurl.OpenLogStore(context.Background(), logStoreURL)
		if err != nil {
			return fmt.Errorf("SPARKWING_LOG_STORE: %w", err)
		}
		opts.LogStore = ls
	}
	if artifactStoreURL != "" {
		as, err := storeurl.OpenArtifactStore(context.Background(), artifactStoreURL)
		if err != nil {
			return fmt.Errorf("SPARKWING_ARTIFACT_STORE: %w", err)
		}
		opts.ArtifactStore = as
	}

	if os.Getenv("SPARKWING_MODE") == "ci-embedded" && logStoreURL == "" && artifactStoreURL == "" {
		fmt.Fprintln(os.Stderr,
			"warn: --mode=ci-embedded with no SPARKWING_LOG_STORE / SPARKWING_ARTIFACT_STORE; "+
				"logs + artifacts will live in this VM's filesystem and not survive job exit")
	}
	return nil
}
