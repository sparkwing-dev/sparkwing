package profile

import (
	"errors"
	"fmt"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

type Profile struct {
	Name             string `yaml:"-"`
	logsURLInherited bool

	Controller *ControllerSpec `yaml:"controller,omitempty"`

	Secrets *backends.Spec `yaml:"secrets,omitempty"`
	State   *backends.Spec `yaml:"state,omitempty"`
	Cache   *backends.Spec `yaml:"cache,omitempty"`
	Logs    *backends.Spec `yaml:"logs,omitempty"`

	MirrorLocal *bool `yaml:"mirror_local,omitempty"`
}

type ControllerSpec struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token,omitempty"`
}

func (p *Profile) ControllerURL() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.URL
}

func (p *Profile) ControllerToken() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.Token
}

// ExplicitLogsURL returns the logs URL the profile supplied, excluding a
// controller URL filled in by InheritControllerDefaults.
func (p *Profile) ExplicitLogsURL() string {
	if p == nil || p.Logs == nil || p.logsURLInherited {
		return ""
	}
	return p.Logs.URL
}

func (p *Profile) HasController() bool {
	return p.ControllerURL() != ""
}

func (p *Profile) InheritControllerDefaults() {
	if p == nil || p.Controller == nil {
		return
	}
	if p.Logs != nil && p.Logs.Type == backends.TypeController && p.Logs.URL == "" {
		p.logsURLInherited = true
	}
	for _, spec := range []*backends.Spec{p.Secrets, p.State, p.Cache, p.Logs} {
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

func (p *Profile) Surfaces() backends.Surfaces {
	if p == nil {
		return backends.Surfaces{}
	}
	return backends.Surfaces{
		Secrets: p.Secrets,
		Cache:   p.Cache,
		Logs:    p.Logs,
		State:   p.State,
	}
}

func (p *Profile) EffectiveMirrorLocal() bool {
	if p == nil || p.MirrorLocal == nil {
		return true
	}
	return *p.MirrorLocal
}

// Config is the profiles section of config.yaml: profile name to profile.
type Config struct {
	Profiles map[string]*Profile
}

var ErrNoProfile = errors.New("no profile configured")

var ErrProfileNotFound = errors.New("profile not found")

// DefaultPath reports the config.yaml profiles are read from and written to;
// see [userconfig.Path].
func DefaultPath() (string, error) {
	return userconfig.Path()
}

// Load reads the profiles section of the config.yaml at path. An absent file
// or section is an empty set of profiles.
func Load(path string) (*Config, error) {
	cfg := Config{Profiles: map[string]*Profile{}}
	if _, err := userconfig.Read(path, userconfig.Profiles, &cfg.Profiles); err != nil {
		return nil, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	for name, p := range cfg.Profiles {
		if p == nil {
			cfg.Profiles[name] = &Profile{Name: name}
			continue
		}
		p.Name = name
		p.InheritControllerDefaults()
		if err := p.validateSurfaceFields(); err != nil {
			return nil, fmt.Errorf("%s: profile %q: %w", path, name, err)
		}
	}
	return &cfg, nil
}

func (p *Profile) validateSurfaceFields() error {
	for surface, spec := range map[string]*backends.Spec{
		"secrets": p.Secrets,
		"state":   p.State,
		"cache":   p.Cache,
		"logs":    p.Logs,
	} {
		if err := spec.ValidateFields(surface); err != nil {
			return err
		}
	}
	if p.Cache != nil && p.Cache.Binaries != nil {
		return p.Cache.Binaries.ValidateFields("cache.binaries")
	}
	return nil
}

// Save replaces the profiles section of the config.yaml at path with cfg,
// keeping every other section; see [userconfig.Write].
func Save(path string, cfg *Config) error {
	out := map[string]*Profile{}
	for name, p := range cfg.Profiles {
		if p == nil {
			continue
		}
		cp := *p
		cp.Name = ""
		if cp.logsURLInherited && cp.Logs != nil {
			logs := *cp.Logs
			logs.URL = ""
			cp.Logs = &logs
		}
		out[name] = &cp
	}
	return userconfig.Write(path, userconfig.Profiles, "profiles", out)
}

func LoadAndResolve(explicitName string) (*Profile, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	p, _, err := Resolve(explicitName, cfg)
	return p, err
}

func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
