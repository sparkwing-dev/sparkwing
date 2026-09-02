package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

type Profile struct {
	Name string `yaml:"-"`

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

func (p *Profile) HasController() bool {
	return p.ControllerURL() != ""
}

func (p *Profile) InheritControllerDefaults() {
	if p == nil || p.Controller == nil {
		return
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

type Config struct {
	Profiles map[string]*Profile `yaml:"profiles,omitempty"`
}

var ErrNoProfile = errors.New("no profile configured")

var ErrProfileNotFound = errors.New("profile not found")

func DefaultPath() (string, error) {
	if v := os.Getenv("SPARKWING_PROFILES"); v != "" {
		return v, nil
	}
	return fssecure.ConfigFile("profiles.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Profiles: map[string]*Profile{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
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

func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := fssecure.EnsureConfigDir(dir); err != nil {
		return fmt.Errorf("prepare %s: %w", dir, err)
	}
	out := &Config{Profiles: map[string]*Profile{}}
	for name, p := range cfg.Profiles {
		if p == nil {
			continue
		}
		cp := *p
		cp.Name = ""
		out.Profiles[name] = &cp
	}
	buf, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal profiles: %w", err)
	}
	// safety: a random name plus O_EXCL keeps a pre-created path from receiving the token.
	f, err := os.CreateTemp(dir, ".profiles-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := fssecure.TightenOpen(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure %s: %w", tmp, err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
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

func HintMissing(err error, cfg *Config) string {
	base := err.Error()
	if cfg != nil && len(cfg.Profiles) > 0 {
		return fmt.Sprintf("%s\n\nAvailable profiles: %v\nPass --profile <name>.",
			base, cfg.Names())
	}
	return fmt.Sprintf("%s\n\nRegister a profile first:\n  sparkwing configure profiles add --name local --controller http://127.0.0.1:4344\nOr point at a remote controller:\n  sparkwing configure profiles add --name prod --controller https://api.example.dev --token swu_...", base)
}
