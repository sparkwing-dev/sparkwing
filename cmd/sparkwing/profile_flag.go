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

func resolveProfileChain(name string) (*profile.Profile, profile.Chain, string, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, profile.Chain{}, "", err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, profile.Chain{}, path, err
	}
	if name == "" {
		pp, defName, ok, perr := projectDefaultProfile()
		if perr != nil {
			return nil, profile.Chain{}, path, perr
		}
		if ok {
			return pp, profile.Chain{Selected: defName, Source: profile.ChainSourceProjectDefault}, path, nil
		}
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

func projectDefaultProfile() (*profile.Profile, string, bool, error) {
	cfg, ok, err := loadProjectConfig()
	if err != nil {
		return nil, "", false, err
	}
	if !ok || cfg.Defaults.Profile == "" || cfg.Profiles == nil {
		return nil, "", false, nil
	}
	p, ok := cfg.Profiles[cfg.Defaults.Profile]
	if !ok || p == nil {
		return nil, "", false, nil
	}
	return p, cfg.Defaults.Profile, true, nil
}

func loadProjectConfig() (*projectconfig.Config, bool, error) {
	dir, err := findSparkwingDir()
	if err != nil {
		return nil, false, nil
	}
	cfg, err := projectconfig.Load(filepath.Join(dir, projectconfig.Filename))
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

const migrationLinkWhereFlag = "https://sparkwing.dev/docs/migration-guide/v0.5.0#-profile-is-the-only-where-flag"

var retiredWhereFlags = map[string]string{
	"--on":         "v0.5.0 replaces --on with --profile.",
	"--sw-on":      "v0.5.0 replaces --sw-on with --profile.",
	"--sw-profile": "v0.5.0 removes --sw-profile; `sparkwing run` always executes locally. Use `sparkwing pipeline trigger --profile X` for remote dispatch.",
	"--sw-target":  "--sw-target was renamed to --target in v0.5.0; same semantics.",
}

func checkRetiredWhereFlags(args []string, owned map[string]bool) error {
	for _, a := range args {
		name := a
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name = a[:eq]
		}
		if owned[strings.TrimPrefix(name, "--")] {
			continue
		}
		if msg, ok := retiredWhereFlags[name]; ok {
			return fmt.Errorf("unknown flag %s. %s\nSee %s", name, msg, migrationLinkWhereFlag)
		}
	}
	return nil
}

func displayConfigPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
