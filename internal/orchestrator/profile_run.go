package orchestrator

import (
	"fmt"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// profileFromEnv resolves the SPARKWING_PROFILE env var (set by the
// outer CLI for `sparkwing run --profile NAME`) to a *profile.Profile
// and the resolution chain that picked it. Returns (nil, nil, nil) when
// SPARKWING_PROFILE is unset, leaving the legacy backends.yaml flow in
// place. Resolution mirrors the outer CLI: load profiles.yaml and
// resolve the name through the chain resolver at the flag level (the
// project hint is wired in a later step). The chain feeds run_start's
// profile block.
func profileFromEnv() (*profile.Profile, *profile.Chain, error) {
	name := os.Getenv("SPARKWING_PROFILE")
	if name == "" {
		return nil, nil, nil
	}
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, nil, err
	}
	p, chain, err := profile.ResolveChain(name, "", cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve profile %q: %w", name, err)
	}
	return p, &chain, nil
}
