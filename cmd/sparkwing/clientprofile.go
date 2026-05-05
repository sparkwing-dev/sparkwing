// Client-command profile resolution. Shared by every human-driven
// subcommand that talks to a remote controller or logs service
// (tokens, users, jobs retry/cancel/prune/logs, gc, fleet-worker,
// cluster-mode web). Each subcommand registers `--on <name>` via
// addProfileFlag, then calls resolveProfile to fetch the connection
// info.
package main

import (
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/profile"
)

// addProfileFlag registers a `--on <name>` flag on fs. The returned
// pointer is populated after fs.Parse. Empty = use the default
// profile.
func addProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("on", "",
		"profile name from ~/.config/sparkwing/profiles.yaml (default: current default)")
}

// resolveProfile loads profiles.yaml, picks the profile per `--on`
// and the file's default, and returns it. On any failure it prints
// a helpful hint to stderr and returns a non-nil error so callers
// can exit 1 without extra formatting.
func resolveProfile(name string) (*profile.Profile, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, err
	}
	p, err := profile.Resolve(cfg, name)
	if err != nil {
		// Format with hint inline so the caller just returns the err
		// and the user sees a full, actionable message.
		fmt.Fprintln(os.Stderr, profile.HintMissing(err, cfg))
		return nil, errors.New("no profile resolved")
	}
	return p, nil
}

// requireController fails fast when a command needs a controller URL
// but the resolved profile lacks one. Not applicable to every
// command -- some (jobs logs) have local-only paths when no profile
// is active -- but common enough to centralize.
func requireController(p *profile.Profile, cmd string) error {
	if p.Controller == "" {
		return fmt.Errorf("%s: profile %q has no controller URL", cmd, p.Name)
	}
	return nil
}
