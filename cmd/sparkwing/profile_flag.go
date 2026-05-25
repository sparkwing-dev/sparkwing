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

// resolveProfileChain loads profiles.yaml and resolves NAME through the
// chain resolver (flag level only; the project hint is wired in step 9).
// It returns the resolved profile, the resolution chain (for the
// `sparkwing profile` introspection command), and the resolved
// profiles.yaml path (for display). A missing profile returns a
// not-found error naming the file and the available profiles. Shared by
// `sparkwing run` / `pipeline trigger` / the read commands (via
// resolveProfileFlag) and `sparkwing profile` so all report the same
// resolution.
func resolveProfileChain(name string) (*profile.Profile, profile.Chain, string, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, profile.Chain{}, "", err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, profile.Chain{}, path, err
	}
	p, chain, err := profile.ResolveChain(name, "", cfg)
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

// profileOnMutualExclusion returns the exit-code-2 error fired when a
// command receives both --profile and the legacy remote-trigger flag.
// otherFlag names the legacy flag for the surface in question
// (--sw-profile on `run`, --on on the read commands) so the message
// points at what the user actually typed.
func profileOnMutualExclusion(otherFlag string) error {
	return exitErrorf(2,
		"--profile and %s are mutually exclusive. Use --profile for local execution "+
			"against a profile's storage; use %s for the legacy remote-trigger path "+
			"(slated for removal in v0.5.0; see docs/migrations/v0.5.0.md)",
		otherFlag, otherFlag)
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
