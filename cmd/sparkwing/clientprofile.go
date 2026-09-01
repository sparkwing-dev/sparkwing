package main

import (
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func addProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "",
		"profile name from ~/.config/sparkwing/profiles.yaml")
}

func resolveProfile(name string) (*profile.Profile, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, err
	}
	p, _, err := profile.Resolve(name, cfg)
	if err == nil && p == nil {
		err = profile.ErrNoProfile
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, profile.HintMissing(err, cfg))
		return nil, errors.New("no profile resolved")
	}
	return p, nil
}

func requireController(p *profile.Profile, cmd string) error {
	if p.ControllerURL() == "" {
		return fmt.Errorf("%s: profile %q has no controller URL", cmd, p.Name)
	}
	return nil
}
