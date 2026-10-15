package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

// safety: only the flag selects a profile, so a command without --profile acts
// on this machine's store whatever the environment or project config holds.
func resolveProfileChain(name string) (*profile.Profile, profile.Chain, string, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, profile.Chain{}, "", err
	}
	if name == "" {
		return nil, profile.Chain{Source: profile.ChainSourceNone}, path, nil
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, profile.Chain{}, path, err
	}
	p, chain, err := profile.Resolve(name, cfg)
	if err != nil {
		if errors.Is(err, profile.ErrProfileNotFound) {
			return nil, profile.Chain{}, path, fmt.Errorf(
				"profile %q not found in %s.\nAvailable profiles: %s",
				name, displayConfigPath(path), strings.Join(cfg.Names(), ", "))
		}
		return nil, profile.Chain{}, path, err
	}
	return p, chain, path, nil
}

func resolveProfileFlag(name string) (*profile.Profile, error) {
	p, _, _, err := resolveProfileChain(name)
	return p, err
}

func displayConfigPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
