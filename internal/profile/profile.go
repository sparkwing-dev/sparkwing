// Package profile manages named storage/connection profiles selected by
// `--profile <name>`. A profile describes where a run's state, cache, and
// logs live (the backend triple) plus any controller URL / token needed
// to reach a remote controller.
//
// Path resolution (first match wins):
//
//  1. $SPARKWING_PROFILES
//  2. $XDG_CONFIG_HOME/sparkwing/profiles.yaml
//  3. $HOME/.config/sparkwing/profiles.yaml
//
// On-disk shape:
//
//	default: laptop
//	profiles:
//	  laptop:
//	    state: { type: sqlite }
//	    cache: { type: filesystem, path: ~/.cache/sparkwing }
//	    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }
//	  prod:
//	    controller: https://api.example.dev
//	    token: swu_...
//	    gitcache: https://gitcache.example.dev
//	    # state/cache/logs implied by the controller when omitted.
//
// Missing optional fields come back as nil specs / empty strings.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

// Profile is one named connection bundle.
type Profile struct {
	Name string `yaml:"-"`

	// Controller bundles the remote controller's URL and bearer token.
	// Nil = laptop-local profile (no remote dispatch). When set, the
	// operator's CLI talks to this controller for triggers, run state,
	// log streaming, and auxiliary-service discovery (the cache pod
	// URL for `sparkwing push` etc.). The token authenticates every
	// request; nil/empty token = no Authorization header sent.
	Controller *ControllerSpec `yaml:"controller,omitempty"`

	// State, Cache, and Logs carry the full backend triple so a profile
	// fully describes where its runs persist. Consume them as a unit via
	// Surfaces. A nil pointer means "not specified at this layer"; a
	// controller-only profile leaves all three nil and routes every
	// surface through its controller.
	State *backends.Spec `yaml:"state,omitempty"`
	Cache *backends.Spec `yaml:"cache,omitempty"`
	Logs  *backends.Spec `yaml:"logs,omitempty"`

	// MirrorLocal toggles whether local execution against this profile
	// also writes state to the local SQLite store. Nil means the
	// default (true); set false for automated workers that fire and
	// forget. Consume via EffectiveMirrorLocal.
	MirrorLocal *bool `yaml:"mirror_local,omitempty"`

	// SourceOverride, when set, wholesale replaces whatever
	// dispatch.source spec a pipeline declared for runs under this
	// profile. Used as a per-user dev/test escape hatch: a developer
	// can point every pipeline at a local dotenv without touching
	// pipeline YAMLs. The override is opaque to the pipeline -- if
	// it's set on the active profile, the pipeline's source spec
	// (URL match included) is not consulted.
	SourceOverride *sources.Source `yaml:"source_override,omitempty"`
}

// ControllerSpec is the nested controller block on a Profile.
type ControllerSpec struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token,omitempty"`
}

// ControllerURL returns the profile's controller URL or "" when no
// controller is configured. Nil-safe at every level so callers don't
// need a Controller != nil check before reading.
func (p *Profile) ControllerURL() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.URL
}

// ControllerToken returns the profile's controller bearer token or
// "" when none is configured. Nil-safe like ControllerURL.
func (p *Profile) ControllerToken() string {
	if p == nil || p.Controller == nil {
		return ""
	}
	return p.Controller.Token
}

// HasController reports whether this profile dispatches to a remote
// controller. Equivalent to ControllerURL() != "" but reads more
// naturally at call sites.
func (p *Profile) HasController() bool {
	return p.ControllerURL() != ""
}

// Surfaces returns the profile's State/Cache/Logs as a
// backends.Surfaces, suitable for handing to the backend factories.
// A nil profile, or one with all three specs unset, yields a
// zero-valued Surfaces so callers can layer it against project /
// built-in surfaces with backends.Effective.
func (p *Profile) Surfaces() backends.Surfaces {
	if p == nil {
		return backends.Surfaces{}
	}
	return backends.Surfaces{
		Cache: p.Cache,
		Logs:  p.Logs,
		State: p.State,
	}
}

// EffectiveMirrorLocal reports whether local execution against this
// profile should mirror state to the local SQLite store. Defaults to
// true when unset, because laptop execution mirrors by default.
// Nil-safe: a nil profile reports true.
func (p *Profile) EffectiveMirrorLocal() bool {
	if p == nil || p.MirrorLocal == nil {
		return true
	}
	return *p.MirrorLocal
}

// Config is the on-disk profiles.yaml file.
type Config struct {
	Default  string              `yaml:"default,omitempty"`
	Profiles map[string]*Profile `yaml:"profiles,omitempty"`
}

// ErrNoProfile is returned by Resolve when no profile can be
// identified.
var ErrNoProfile = errors.New("no profile configured")

// ErrProfileNotFound is returned when --on names a profile that
// doesn't exist in profiles.yaml.
var ErrProfileNotFound = errors.New("profile not found")

// DefaultPath returns the resolved profiles.yaml path, honoring
// SPARKWING_PROFILES > XDG_CONFIG_HOME > $HOME.
func DefaultPath() (string, error) {
	if v := os.Getenv("SPARKWING_PROFILES"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sparkwing", "profiles.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve profiles.yaml path: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "profiles.yaml"), nil
}

// Load reads and parses profiles.yaml at path. Missing file returns
// an empty Config without error; parse errors are surfaced.
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
	// Stamp .Name from the map key so Resolve returns a fully-formed
	// Profile without a separate lookup.
	for name, p := range cfg.Profiles {
		if p == nil {
			cfg.Profiles[name] = &Profile{Name: name}
		} else {
			p.Name = name
		}
	}
	return &cfg, nil
}

// Save writes cfg to path atomically (write tmp, rename). Mode 0600
// because profiles.yaml carries bearer tokens in plaintext.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Strip .Name before marshal (duplicates the map key).
	out := &Config{Default: cfg.Default, Profiles: map[string]*Profile{}}
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// LoadAndResolve does DefaultPath + Load + Resolve in one call,
// resolving explicitName through the chain (flag level; no project
// hint). A nil profile is never returned for an empty name: the chain
// falls through to the default and finally the built-in laptop profile.
func LoadAndResolve(explicitName string) (*Profile, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	p, _, err := Resolve(explicitName, "", cfg)
	return p, err
}

// Names returns the profile names sorted alphabetically.
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// HintMissing formats a human-readable error body pointing the
// operator at next steps.
func HintMissing(err error, cfg *Config) string {
	base := err.Error()
	if cfg != nil && len(cfg.Profiles) > 0 {
		return fmt.Sprintf("%s\n\nAvailable profiles: %v\nPass --profile <name>, or set a default via `sparkwing profiles use <name>`.",
			base, cfg.Names())
	}
	return fmt.Sprintf("%s\n\nRegister a profile first:\n  sparkwing profiles add local --controller http://127.0.0.1:4344\nOr point at a remote controller:\n  sparkwing profiles add prod --controller https://api.example.dev --token swu_...", base)
}
