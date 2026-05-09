// Package profile manages named bundles of controller connection info
// (URL, logs URL, token, gitcache URL) used by `--on <name>`.
//
// Path resolution (first match wins):
//
//  1. $SPARKWING_PROFILES
//  2. $XDG_CONFIG_HOME/sparkwing/profiles.yaml
//  3. $HOME/.config/sparkwing/profiles.yaml
//
// On-disk shape:
//
//	default: local
//	profiles:
//	  local:
//	    controller: http://127.0.0.1:4344
//	    logs: http://127.0.0.1:4345
//	  prod:
//	    controller: https://api.example.dev
//	    logs: https://logs.example.dev
//	    token: swu_...
//	    gitcache: https://gitcache.example.dev
//
// Missing optional fields come back as empty strings.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Profile is one named connection bundle.
type Profile struct {
	Name       string `yaml:"-"`
	Controller string `yaml:"controller,omitempty"`
	Logs       string `yaml:"logs,omitempty"`
	Token      string `yaml:"token,omitempty"`
	Gitcache   string `yaml:"gitcache,omitempty"`

	// Pluggable storage URLs. Accepts fs:///abs/path or
	// s3://bucket/prefix.
	LogStore      string `yaml:"log_store,omitempty"`
	ArtifactStore string `yaml:"artifact_store,omitempty"`

	// CostPerRunnerHour feeds the simple compute-time × rate cost
	// shown on `sparkwing runs receipt`. USD; default 0 reports
	// compute_cents=0 instead of a misleading cost. Cloud-billing
	// reconciliation layers on top.
	CostPerRunnerHour float64 `yaml:"cost_per_runner_hour,omitempty"`

	// AutoAllow pre-authorizes per-marker blast-radius gates for
	// this profile. A low-stakes environment (laptop, kind
	// cluster) can declare `auto_allow: { destructive: true }` so an
	// operator running `wing destroy-cluster --on laptop` doesn't
	// have to pass --allow-destructive every time. Production
	// profiles should leave this zero so the gate stays loud.
	// Defaults are zero everywhere; the field is opt-in.
	AutoAllow AutoAllow `yaml:"auto_allow,omitempty"`
}

// AutoAllow is the per-marker pre-authorization block declared
// inside a Profile. Each field maps to one BlastRadius marker:
// destructive (BlastRadiusDestructive), production
// (BlastRadiusAffectsProduction), money (BlastRadiusCostsMoney).
type AutoAllow struct {
	Destructive bool `yaml:"destructive,omitempty"`
	Production  bool `yaml:"production,omitempty"`
	Money       bool `yaml:"money,omitempty"`
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

// Resolve returns the profile matching the caller's intent:
//
//   - explicitName non-empty -> look up that profile.
//   - else cfg.Default if set.
//   - else ErrNoProfile.
//
// The returned pointer is owned by cfg.
func Resolve(cfg *Config, explicitName string) (*Profile, error) {
	if cfg == nil {
		return nil, ErrNoProfile
	}
	name := explicitName
	if name == "" {
		name = cfg.Default
	}
	if name == "" {
		return nil, ErrNoProfile
	}
	p, ok := cfg.Profiles[name]
	if !ok || p == nil {
		return nil, fmt.Errorf("%w: %q", ErrProfileNotFound, name)
	}
	return p, nil
}

// LoadAndResolve does DefaultPath + Load + Resolve in one call.
func LoadAndResolve(explicitName string) (*Profile, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return Resolve(cfg, explicitName)
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
		return fmt.Sprintf("%s\n\nAvailable profiles: %v\nPass --on <name>, or set a default via `sparkwing profiles use <name>`.",
			base, cfg.Names())
	}
	return fmt.Sprintf("%s\n\nRegister a profile first:\n  sparkwing profiles add local --controller http://127.0.0.1:4344 --logs http://127.0.0.1:4345\nOr point at a remote controller:\n  sparkwing profiles add prod --controller https://api.example.dev --token swu_...", base)
}
