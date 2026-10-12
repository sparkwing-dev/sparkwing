package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ProfileEnv and SecretsProfileEnv name the variables a pipeline binary built
// against an older SDK reads to select a profile and a secrets source. Nothing
// here reads them; the CLI and the run consumer drop them before starting a
// pipeline process, so an exported value cannot point an older binary at a
// remote store.
const (
	ProfileEnv        = "SPARKWING_PROFILE"
	SecretsProfileEnv = "SPARKWING_SECRETS_PROFILE"
)

// safety: only a CLI from before the removal of run --profile sets ProfileEnv,
// and it means "record this run to that profile's backends". Running locally
// instead would record somewhere the caller did not ask for, so the run fails.
func refuseProfileHandoff() error {
	name := os.Getenv(ProfileEnv)
	if name == "" {
		return nil
	}
	return fmt.Errorf("%s=%s asks this run to record to a profile, which this pipeline's SDK no longer does; "+
		"upgrade the sparkwing CLI, or run on the profile's controller with sparkwing pipeline trigger <pipeline> --profile %s",
		ProfileEnv, name, name)
}

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
// the pipeline binary, because a binary built against an older SDK skips the
// check in handle-trigger. A config that cannot be read refuses the run.
func refusePinnedIn(sparkwingDir, pipeline string) error {
	path := filepath.Join(sparkwingDir, projectconfig.Filename)
	cfg, err := projectconfig.Load(path)
	if err != nil {
		return fmt.Errorf("read %s to check pipeline %s's profile pin: %w", path, pipeline, err)
	}
	if cfg == nil {
		return nil
	}
	return RefusePinnedLocalRun((&pipelines.Config{Pipelines: cfg.Pipelines}).Find(pipeline))
}

// safety: a config that cannot be read hides the pin, so the run fails rather
// than executing a pipeline that may be pinned to a controller.
func refusePinnedCheckoutRun(entry *pipelines.Pipeline) error {
	if _, path, err := loadCheckoutProjectConfig(); err != nil {
		return fmt.Errorf("read %s to check the pipeline's profile pin: %w", path, err)
	}
	return RefusePinnedLocalRun(entry)
}
