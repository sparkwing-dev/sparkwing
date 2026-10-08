package orchestrator

import (
	"errors"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ProfileEnv hands the --profile of a `sparkwing run` invocation to the
// pipeline process the CLI execs. The CLI writes it only from that flag, and
// only for a remote-state profile, and clears an inherited value first, so an
// exported variable never selects a profile.
const ProfileEnv = "SPARKWING_PROFILE"

// SecretsProfileEnv names the variable a pipeline binary built against an
// older SDK reads to pick a controller secrets source. Nothing here reads it;
// the CLI and the run consumer drop it before starting a pipeline process.
const SecretsProfileEnv = "SPARKWING_SECRETS_PROFILE"

func handoffProfile() string { return os.Getenv(ProfileEnv) }

func resolveActiveProfile(projectCfg *projectconfig.Config) (*profile.Profile, *profile.Chain, error) {
	name := handoffProfile()
	if name == "" {
		return nil, &profile.Chain{Source: profile.ChainSourceNone}, nil
	}
	p, chain, err := resolveNamedProfile(name, projectCfg)
	if err != nil {
		return nil, nil, err
	}
	if err := RemoteStateProfile(p); err != nil {
		return nil, nil, err
	}
	return p, chain, nil
}

// safety: a coordinated node replans its local dispatcher's run, so it must see
// the locality and profile that dispatcher saw; any other node runs where a
// controller dispatched it.
func nodeProfileResolution(coordinated bool) sparkwing.ProfileResolutionContext {
	if !coordinated {
		return sparkwing.ProfileResolutionContext{}
	}
	return sparkwing.ProfileResolutionContext{Name: handoffProfile(), IsLocal: true}
}

// RemoteStateProfile reports whether `sparkwing run --profile` may use p: only
// a profile with no controller whose state is an S3 bucket or a Postgres
// database. This is a temporary exception for teams that coordinate through
// shared storage; every other profile runs on its controller through
// `sparkwing pipeline trigger`.
func RemoteStateProfile(p *profile.Profile) error {
	if p == nil {
		return nil
	}
	state := p.Surfaces().State
	if p.ControllerURL() == "" && state != nil && (state.Type == backends.TypeS3 || state.Type == backends.TypePostgres) {
		return nil
	}
	return fmt.Errorf("profile %q is not an S3 or Postgres state profile; sparkwing run executes locally, so run on its controller with sparkwing pipeline trigger <pipeline> --profile %s",
		p.Name, p.Name)
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

// safety: a config that cannot be read hides the pin, so the run fails rather
// than executing a pipeline that may be pinned to a controller.
func refusePinnedCheckoutRun(entry *pipelines.Pipeline) error {
	if _, path, err := loadCheckoutProjectConfig(); err != nil {
		return fmt.Errorf("read %s to check the pipeline's profile pin: %w", path, err)
	}
	return RefusePinnedLocalRun(entry)
}

func fleetProfileUsesRemoteAuthority(p *profile.Profile) bool {
	if p == nil {
		return false
	}
	if p.HasController() {
		return true
	}
	surfaces := p.Surfaces()
	if !fleetSurfaceLocal(surfaces.Secrets, backends.TypeEnv, backends.TypeFilesystem, backends.TypeNone) ||
		!fleetSurfaceLocal(surfaces.State, backends.TypeSQLite) ||
		!fleetSurfaceLocal(surfaces.Cache, backends.TypeFilesystem) ||
		!fleetSurfaceLocal(surfaces.Logs, backends.TypeFilesystem, backends.TypeStdout) {
		return true
	}
	return !fleetSurfaceLocal(surfaces.BinaryCache(), backends.TypeFilesystem)
}

func fleetSurfaceLocal(spec *backends.Spec, allowed ...string) bool {
	if spec == nil {
		return true
	}
	for _, typ := range allowed {
		if spec.Type == typ {
			return true
		}
	}
	return false
}

func resolveNamedProfile(name string, projectCfg *projectconfig.Config) (*profile.Profile, *profile.Chain, error) {
	p, chain, userErr := resolveUserProfile(name)
	if userErr == nil {
		return p, chain, nil
	}
	if !errors.Is(userErr, profile.ErrProfileNotFound) {
		return nil, nil, userErr
	}
	if projectCfg != nil && projectCfg.Profiles != nil {
		if pp, ok := projectCfg.Profiles[name]; ok && pp != nil {
			return pp, &profile.Chain{Selected: name, Source: profile.ChainSourceFlag}, nil
		}
	}
	return nil, nil, fmt.Errorf("--profile %s: %w (checked %s and the project's profiles: block)",
		name, profile.ErrProfileNotFound, userProfilesPathForError())
}

func userProfilesPathForError() string {
	path, err := profile.DefaultPath()
	if err != nil {
		return "config.yaml"
	}
	return path
}

func resolveUserProfile(name string) (*profile.Profile, *profile.Chain, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, nil, err
	}
	p, chain, err := profile.Resolve(name, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("--profile %s: %w", name, err)
	}
	return p, &chain, nil
}
