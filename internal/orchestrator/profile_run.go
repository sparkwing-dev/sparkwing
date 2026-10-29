package orchestrator

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// RefusePinnedLocalRun refuses local execution of a pipeline whose
// sparkwing.yaml entry pins it to a controller profile.
func RefusePinnedLocalRun(p *pipelines.Pipeline) error {
	if p == nil || p.Profile == "" {
		return nil
	}
	return fmt.Errorf("pipeline %s is pinned to profile %s; use sparkwing pipeline trigger %s --profile %s",
		p.Name, p.Profile, p.Name, p.Profile)
}
