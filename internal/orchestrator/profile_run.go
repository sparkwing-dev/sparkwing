package orchestrator

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// profileFromEnv resolves the active storage profile for a local run and
// the resolution chain that picked it. The chain runs at the flag level
// (SPARKWING_PROFILE, set by the outer CLI for `--profile NAME`), the
// project hint (.sparkwing/sparkwing.yaml `profile:`), profiles.yaml
// `default:`, any matching `detect:` block, and finally the built-in
// laptop fallback -- so it always returns a non-nil profile. The chain
// feeds run_start's profile block.
func profileFromEnv() (*profile.Profile, *profile.Chain, error) {
	name := os.Getenv("SPARKWING_PROFILE")
	hint := projectProfileHint()
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, nil, err
	}
	p, chain, err := profile.Resolve(name, hint, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve profile: %w", err)
	}
	return p, &chain, nil
}

// projectProfileHint reads .sparkwing/sparkwing.yaml's profile: field
// (discovered by walking up from cwd) -- the project-level resolution
// hint that sits below an explicit --profile flag. Returns "" when no
// sparkwing.yaml is found or it declares no profile:.
func projectProfileHint() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	_, cfg, err := projectconfig.Discover(cwd)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Profile
}
