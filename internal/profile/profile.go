package profile

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

type profile struct {
	Name string `yaml:"-"`

	Controller *controllerSpec `yaml:"controller,omitempty"`

	Secrets *backends.Spec `yaml:"secrets,omitempty"`
	State   *backends.Spec `yaml:"state,omitempty"`
}

type controllerSpec struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token,omitempty"`
}

func (p *profile) controllerURL() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.URL
}

func (p *profile) controllerToken() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.Token
}

func (p *profile) inheritControllerDefaults() {
	if p == nil || p.Controller == nil {
		return
	}
	for _, spec := range []*backends.Spec{p.Secrets, p.State} {
		if spec == nil || spec.Type != backends.TypeController {
			continue
		}
		if spec.URL == "" {
			spec.URL = p.Controller.URL
		}
		if spec.Token == "" && spec.TokenEnv == "" {
			spec.Token = p.Controller.Token
		}
		if spec.Controller == "" && p.Name != "" {
			spec.Controller = p.Name
		}
	}
}

func (p *profile) surfaces() backends.Surfaces {
	if p == nil {
		return backends.Surfaces{}
	}
	return backends.Surfaces{
		Secrets: p.Secrets,
		State:   p.State,
	}
}

// config is the profiles section of config.yaml: profile name to profile.
type config struct {
	Profiles map[string]*profile
}

var errProfileNotFound = errors.New("profile not found")

// defaultPath reports the config.yaml profiles are read from and written to;
// see [userconfig.Path].
func defaultPath() (string, error) {
	return userconfig.Path()
}

// load reads the profiles section of the config.yaml at path. An absent file
// or section is an empty set of profiles.
func load(path string) (*config, error) {
	cfg := config{Profiles: map[string]*profile{}}
	if _, err := userconfig.Read(path, userconfig.Profiles, &cfg.Profiles); err != nil {
		return nil, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*profile{}
	}
	for name, p := range cfg.Profiles {
		if p == nil {
			cfg.Profiles[name] = &profile{Name: name}
			continue
		}
		p.Name = name
		p.inheritControllerDefaults()
		if err := p.validateSurfaceFields(); err != nil {
			return nil, fmt.Errorf("%s: profile %q: %w", path, name, err)
		}
	}
	return &cfg, nil
}

func (p *profile) validateSurfaceFields() error {
	for surface, spec := range map[string]*backends.Spec{
		"secrets": p.Secrets,
		"state":   p.State,
	} {
		if err := spec.ValidateFields(surface); err != nil {
			return err
		}
	}
	return nil
}

// save replaces the profiles section of the config.yaml at path with cfg,
// keeping every other section; see [userconfig.Write].
func save(path string, cfg *config) error {
	out := map[string]*profile{}
	for name, p := range cfg.Profiles {
		if p == nil {
			continue
		}
		cp := *p
		cp.Name = ""
		out[name] = &cp
	}
	return userconfig.Write(path, userconfig.Profiles, "profiles", out)
}

func (c *config) names() []string {
	out := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
