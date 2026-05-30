package orchestrator

import (
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// decodeTargetBackend returns the per-pipeline backend overrides.
// Pipeline-level overrides were dropped in v0.6; this returns a
// zero-valued Surfaces. Kept as a seam in case per-pipeline overrides
// come back later.
func decodeTargetBackend(_ *pipelines.Pipeline, _ string) backends.Surfaces {
	return backends.Surfaces{}
}
