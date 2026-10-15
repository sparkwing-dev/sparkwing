package main

import (
	"errors"
	"fmt"

	flag "github.com/spf13/pflag"
)

var errNoProfile = errors.New("no profile configured")

func addProfileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "",
		"profile name from user config or this project")
}

func resolveProfile(name string) (*namedProfile, error) {
	p, err := resolveProfileFlag(name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errNoProfile
	}
	return p, nil
}

func requireController(p *namedProfile, cmd string) error {
	if p.ControllerURL() == "" {
		return fmt.Errorf("%s: profile %q has no controller URL", cmd, p.Name)
	}
	return nil
}
