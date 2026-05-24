package orchestrator

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// profileFromEnv resolves the SPARKWING_PROFILE env var (set by the
// outer CLI for `sparkwing run --profile NAME`) to a *profile.Profile.
// Returns (nil, nil) when SPARKWING_PROFILE is unset, leaving the legacy
// backends.yaml flow in place. Resolution mirrors the outer CLI: load
// profiles.yaml and resolve the name through the chain resolver at the
// flag level (the project hint is wired in a later step).
func profileFromEnv() (*profile.Profile, error) {
	name := os.Getenv("SPARKWING_PROFILE")
	if name == "" {
		return nil, nil
	}
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, err
	}
	p, _, err := profile.ResolveChain(name, "", cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve profile %q: %w", name, err)
	}
	return p, nil
}
