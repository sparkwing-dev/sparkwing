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
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
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
	p, chain, err := profile.Resolve(name, cfg)
	if err != nil {
		if errors.Is(err, profile.ErrProfileNotFound) {
			return nil, profile.Chain{}, path, fmt.Errorf("profile %q not found in %s.\nAvailable profiles: %s",
				name, displayConfigPath(path), strings.Join(cfg.Names(), ", "))
		}
		return nil, profile.Chain{}, path, err
	}
	return p, chain, path, nil
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
func checkRetiredWhereFlags(args []string) error {
	for _, a := range args {
		name := a
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name = a[:eq]
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
