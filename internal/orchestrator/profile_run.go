package orchestrator

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// profileFromEnv resolves the active storage profile for a local run
// from SPARKWING_PROFILE (set by the outer CLI for `--profile NAME`).
// Returns (nil, nil, nil) when no profile is set -- the orchestrator
// then falls back to the project's default backends.
func profileFromEnv() (*profile.Profile, *profile.Chain, error) {
	name := os.Getenv("SPARKWING_PROFILE")
	if name == "" {
		return nil, &profile.Chain{Source: profile.ChainSourceNone}, nil
	}
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
		return nil, nil, fmt.Errorf("resolve profile: %w", err)
	}
	return p, &chain, nil
}
