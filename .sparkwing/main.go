// Command sparkwing-pipelines is the public sparkwing repo's pipeline
// runner. `sparkwing run <name>` invokes this binary with the pipeline
// name as the first positional arg; runner.Main dispatches to the
// registered pipeline.
//
// This .sparkwing/ tree is intentionally minimal: it covers the
// build / lint / test / static-analysis / release jobs that operate
// on the public OSS code (SDK, CLI, embedded docs).
package main

import (
	_ "sparkwing-pipelines/jobs"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
)

func main() { runner.Main() }
