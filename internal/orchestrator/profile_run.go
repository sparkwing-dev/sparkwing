package orchestrator

import (
	"errors"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func resolveActiveProfile(pipelineYAML *pipelines.Pipeline, projectCfg *projectconfig.Config) (*profile.Profile, *profile.Chain, error) {
	if name := os.Getenv("SPARKWING_PROFILE"); name != "" {
		return resolveNamedProfile(name, projectCfg)
	}
	if pipelineYAML != nil && pipelineYAML.Profile != "" {
		return resolveProjectProfile(pipelineYAML.Profile, projectCfg, "pipeline")
	}
	if projectCfg != nil && projectCfg.Defaults.Profile != "" {
		return resolveProjectProfile(projectCfg.Defaults.Profile, projectCfg, "defaults.profile")
	}
	return nil, &profile.Chain{Source: profile.ChainSourceNone}, nil
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
	return surfaces.Cache != nil && surfaces.Cache.Binaries != nil && !fleetSurfaceLocal(surfaces.Cache.Binaries, backends.TypeFilesystem)
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
		return "profiles.yaml"
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

func resolveProjectProfile(name string, cfg *projectconfig.Config, origin string) (*profile.Profile, *profile.Chain, error) {
	if cfg == nil || cfg.Profiles == nil {
		return nil, nil, fmt.Errorf("%s names profile %q but sparkwing.yaml declares no profiles", origin, name)
	}
	p, ok := cfg.Profiles[name]
	if !ok || p == nil {
		return nil, nil, fmt.Errorf("%s names profile %q which is not declared in sparkwing.yaml profiles", origin, name)
	}
	return p, &profile.Chain{Selected: name, Source: profile.ChainSourceFlag}, nil
}
