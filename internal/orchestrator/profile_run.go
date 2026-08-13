package orchestrator

import (
	"errors"
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// resolveActiveProfile picks the profile that applies to this run.
// Precedence (highest first):
//
//  1. SPARKWING_PROFILE env var (forwarded from --profile NAME) →
//     looks up in ~/.config/sparkwing/profiles.yaml, then in
//     projectCfg.Profiles.
//  2. pipelineYAML.Profile (the pipeline's own profile: field) →
//     looks up in projectCfg.Profiles.
//  3. projectCfg.Profile (the project's top-level profile: default)
//     → looks up in projectCfg.Profiles.
//
// Returns (nil, &Chain{Source: ChainSourceNone}, nil) when none of
// the layers selects a profile -- the orchestrator then runs against
// a minimal sqlite-only shape (test/dev fallback).
//
// A named selection that doesn't resolve to a declared profile is an
// error: the user typo'd or the project file is missing an entry.
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

// resolveNamedProfile looks a --profile selection up in the user's
// profiles.yaml and then in the project's own profiles: block.
//
// Two files declare profiles under one flag name, and before this
// fallback existed they were disjoint namespaces -- a profile written
// into sparkwing.yaml ran under `sparkwing run x` (which reaches it
// through defaults.profile) but was unreachable by `--profile x`,
// which every doc example teaches. The user file wins a name collision
// because it is the operator's own override of what the repository
// ships.
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

// userProfilesPathForError names the user profiles file in a
// not-found message, degrading to the generic name when the path
// cannot be resolved -- an unresolvable path must not replace the
// error the caller actually needs to read.
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
