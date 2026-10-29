package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type namedProfile struct {
	Name string
	userconfig.Profile
}

func (p *namedProfile) ControllerURL() string {
	if p == nil {
		return ""
	}
	return p.Controller.URL
}

func (p *namedProfile) ControllerToken() string {
	if p == nil || p.Controller.Token == nil {
		return ""
	}
	return *p.Controller.Token
}

func loadProfiles() (map[string]userconfig.Profile, string, error) {
	path, err := userconfig.Path()
	if err != nil {
		return nil, "", err
	}
	cfg, err := userconfig.ReadConfig(path)
	return cfg.Profiles, path, err
}

func profileNames(profiles map[string]userconfig.Profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// safety: only the flag selects a profile, so a command without --profile acts
// on this machine's store whatever the environment or project config holds.
func resolveProfileFlag(name string) (*namedProfile, error) {
	if name == "" {
		return nil, nil
	}
	profiles, path, err := loadProfiles()
	if err != nil {
		return nil, err
	}
	p, found := profiles[name]
	if !found {
		return nil, missingProfile(name, path, profiles)
	}
	return &namedProfile{Name: name, Profile: p}, nil
}

func missingProfile(name, path string, profiles map[string]userconfig.Profile) error {
	return &apierr.Error{Code: apierr.NotFound, Message: fmt.Sprintf("profile %q not found in %s.\nAvailable profiles: %s",
		name, displayConfigPath(path), strings.Join(profileNames(profiles), ", "))}
}

func displayConfigPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}
