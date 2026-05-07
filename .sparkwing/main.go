// Command sparkwing-pipelines is the public sparkwing repo's pipeline
// runner. `wing <name>` invokes this binary with the pipeline name as
// the first positional arg; orchestrator.Main dispatches to the
// registered pipeline.
//
// This .sparkwing/ tree is intentionally minimal: it covers the
// build / lint / test / static-analysis / release jobs that operate
// on the public OSS code (SDK, CLI, embedded docs).
package main

import (
	"github.com/sparkwing-dev/sparkwing/orchestrator"

	// Side-effect imports: each jobs/ file's init() registers its
	// pipeline with the sparkwing package's process-global registry.
	_ "sparkwing-pipelines/jobs"
)

func main() { orchestrator.Main() }
