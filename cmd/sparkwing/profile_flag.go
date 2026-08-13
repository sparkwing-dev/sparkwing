// --profile flag resolution (v0.5.0 config redesign). Distinct from
// the legacy --on / --sw-profile remote-trigger path: --profile names a
// storage profile and routes state/logs/cache through it (with a local
// SQLite mirror for non-local profiles). Shared by `sparkwing run` and
// the `runs list/status/logs` read commands.
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

// resolveProfileChain loads profiles.yaml and resolves NAME. Returns
// the resolved profile (nil when name is empty -- no profile active),
// the resolution chain (for the `sparkwing profile` introspection
// command), and the resolved profiles.yaml path (for display). A
// missing named profile returns a not-found error naming the file and
// the available profiles.
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

// projectProfile looks NAME up in the project's own profiles: block,
// which is the second namespace --profile addresses. Any failure to
// find the project at all is reported as "no such project profile":
// the caller is already on its not-found path, and a working directory
// outside a project is one of the ordinary ways to reach it.
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

// projectDefaultProfile resolves the project's defaults.profile: the
// selection `sparkwing run` makes when no --profile is passed.
//
// The read commands consult it for the same reason they honor
// --profile: a run whose state went to the project's default store and
// a `runs status` that reads the local one disagree about where the
// run is, and the operator has no way to ask which is right.
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

// loadProjectConfig reads the project's sparkwing.yaml. A cwd outside
// any project reports false with no error -- "the project selects
// nothing" is the right answer there. A project whose config will not
// load returns the error rather than the same false, because a profile
// silently missing because of a typo three lines away is the failure
// this whole resolution path keeps producing.
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

// resolveProfileFlag is the connection-side use of resolveProfileChain:
// it returns just the resolved profile (the chain is for introspection).
// The caller returns the error as-is; main() prints it under the
// "sparkwing error:" prefix and exits 1.
func resolveProfileFlag(name string) (*profile.Profile, error) {
	p, _, _, err := resolveProfileChain(name)
	return p, err
}

// migrationLinkWhereFlag points at the v0.5.0 guide section covering the
// retired "where" flags.
const migrationLinkWhereFlag = "https://sparkwing.dev/docs/migration-guide/v0.5.0#-profile-is-the-only-where-flag"

// retiredWhereFlags maps a removed or renamed flag to its one-line
// migration pointer. --on / --sw-on / --sw-profile are gone (storage
// addressing is --profile; remote dispatch is `sparkwing pipeline
// trigger`); --sw-target was renamed to --target with identical
// semantics.
var retiredWhereFlags = map[string]string{
	"--on":         "v0.5.0 replaces --on with --profile.",
	"--sw-on":      "v0.5.0 replaces --sw-on with --profile.",
	"--sw-profile": "v0.5.0 removes --sw-profile; `sparkwing run` always executes locally. Use `sparkwing pipeline trigger --profile X` for remote dispatch.",
	"--sw-target":  "--sw-target was renamed to --target in v0.5.0; same semantics.",
}

// checkRetiredWhereFlags scans args for a flag the v0.5.0 cut removed or
// renamed and, when found, returns a migration-pointer error instead of
// letting the standard "unknown flag" handler fire with no guidance.
//
// owned names flags the current command declares itself; those are
// skipped, because a retired global spelling is only wrong where
// nothing else claims it.
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

// displayConfigPath collapses a leading $HOME to ~ so error messages
// match the documented ~/.config/sparkwing/profiles.yaml form instead
// of leaking an absolute home path.
func displayConfigPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
