package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
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

// safety: the dispatcher checks the execution checkout's pin before it starts
// the pipeline, because the pipeline process never checks it. A config that
// cannot be read refuses the run.
func refusePinnedIn(sparkwingDir, pipeline string) error {
	path := filepath.Join(sparkwingDir, projectconfig.Filename)
	cfg, err := projectconfig.LoadOld(path)
	if err != nil {
		return fmt.Errorf("read %s to check pipeline %s's profile pin: %w", path, pipeline, err)
	}
	if cfg == nil {
		return nil
	}
	return RefusePinnedLocalRun((&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline))
}
