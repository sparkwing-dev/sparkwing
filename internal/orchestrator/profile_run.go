package orchestrator

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// RefusePinnedLocalRun returns an error when the pipeline's sparkwing.yaml
// entry pins it to a profile: a pinned pipeline runs only on that profile's
// controller, through `sparkwing pipeline trigger`.
func RefusePinnedLocalRun(p *pipelines.Pipeline) error {
	if p == nil || p.Profile == "" {
		return nil
	}
	return fmt.Errorf("pipeline %s is pinned to profile %s; use sparkwing pipeline trigger %s --profile %s",
		p.Name, p.Profile, p.Name, p.Profile)
}
