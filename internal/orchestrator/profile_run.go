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
		return resolveDefaultProfile(projectCfg.Defaults.Profile, projectCfg)
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

// safety: defaults.profile may name a connection `sparkwing cloud connect`
// wrote to the user's profiles.yaml, which is where its token stays, so the
// project block is preferred but not required.
func resolveDefaultProfile(name string, cfg *projectconfig.Config) (*profile.Profile, *profile.Chain, error) {
	if cfg != nil && cfg.Profiles != nil {
		if p, ok := cfg.Profiles[name]; ok && p != nil {
			return p, &profile.Chain{Selected: name, Source: profile.ChainSourceProjectDefault}, nil
		}
	}
	p, chain, err := resolveUserProfile(name)
	if err != nil {
		if !errors.Is(err, profile.ErrProfileNotFound) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("defaults.profile names profile %q, which is declared neither in sparkwing.yaml profiles nor in %s",
			name, userProfilesPathForError())
	}
	return p, &profile.Chain{Selected: chain.Selected, Source: profile.ChainSourceProjectDefault}, nil
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
