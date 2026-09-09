package main

import (
	"fmt"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func addProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "",
		"profile name from user config or this project")
}

func resolveProfile(name string) (*profile.Profile, error) {
	p, err := resolveProfileFlag(name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, profile.ErrNoProfile
	}
	return p, nil
}

func requireController(p *profile.Profile, cmd string) error {
	if p.ControllerURL() == "" {
		return fmt.Errorf("%s: profile %q has no controller URL", cmd, p.Name)
	}
	return nil
}
