package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
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
			pp, ok, perr := projectProfile(name)
			if perr != nil {
				return nil, profile.Chain{}, path, perr
			}
			if ok {
				return pp, profile.Chain{Selected: name, Source: profile.ChainSourceFlag}, path, nil
			}
			return nil, profile.Chain{}, path, fmt.Errorf(
				"profile %q not found in %s, nor in this project's sparkwing.yaml profiles.\nAvailable profiles: %s",
				name, displayConfigPath(path), strings.Join(cfg.Names(), ", "))
		}
		return nil, profile.Chain{}, path, err
	}
	return p, chain, path, nil
}

func projectProfile(name string) (*profile.Profile, bool, error) {
	cfg, ok, err := loadProjectConfig()
	if err != nil {
		return nil, false, err
	}
	if !ok || cfg.Profiles == nil {
		return nil, false, nil
	}
	p, ok := cfg.Profiles[name]
	if !ok || p == nil {
		return nil, false, nil
	}
	return p, true, nil
}

func loadProjectConfig() (*projectconfig.ConfigOld, bool, error) {
	dir, err := findSparkwingDir()
	if err != nil {
		return nil, false, nil
	}
	cfg, err := projectconfig.LoadOld(filepath.Join(dir, projectconfig.Filename))
	if err != nil {
		return nil, false, err
	}
	if cfg == nil {
		return nil, false, nil
	}
	return cfg, true, nil
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
