package orchestrator

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// safety: a config that cannot be read hides the pin, so the run fails rather
// than executing a pipeline that may be pinned to a controller.
func refusePinnedCheckoutRun(entry *pipelines.Pipeline) error {
	if _, path, err := loadCheckoutProjectConfig(); err != nil {
		return fmt.Errorf("read %s to check the pipeline's profile pin: %w", path, err)
	}
	return RefusePinnedLocalRun(entry)
}

// ProfileEnv and SecretsProfileEnv name the variables a pipeline binary built
// against an older SDK reads to select a remote profile or secrets source.
// The CLI and the run consumer drop them before starting a pipeline process,
// so an exported value never sends a local run to a remote.
const (
	ProfileEnv        = "SPARKWING_PROFILE"
	SecretsProfileEnv = "SPARKWING_SECRETS_PROFILE"
)

// RefusePinnedLocalRun returns an error when the pipeline's sparkwing.yaml
// entry pins it to a profile: a pinned pipeline runs only on that profile's
// controller, through `sparkwing pipeline trigger`.
func RefusePinnedLocalRun(p *pipelines.Pipeline) error {
	if p == nil || p.Profile == "" {
		return nil
	}
	return fmt.Errorf("pipeline %s is pinned to profile %s; use sparkwing pipeline trigger --profile %s %s",
		p.Name, p.Profile, p.Profile, p.Name)
}
