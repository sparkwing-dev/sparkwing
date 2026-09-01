package orchestrator

import (
	"fmt"
	"os"
	"strconv"
)

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
