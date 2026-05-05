// Package profile is the single source of truth for "which
// sparkwing controller am I talking to, with what token."
//
// A profile is a named bundle of connection info: controller URL,
// logs-service URL, auth token, optional gitcache URL. Users
// register one or more profiles in `~/.config/sparkwing/profiles.yaml`
// and select one per command via `--on <name>`. The CLI never
// accepts raw `--controller` / `--token` flags on client commands:
// the profile is the only connection-config surface, which keeps the
// experience unambiguous (one profile in flight, visible at a glance
// in the command line).
//
// Profile config path resolution (first match wins):
//
//  1. $SPARKWING_PROFILES if set
//  2. $XDG_CONFIG_HOME/sparkwing/profiles.yaml if set
//  3. $HOME/.config/sparkwing/profiles.yaml
//
// The on-disk shape:
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
// Missing optional fields (logs, gitcache, token) come back as empty
// strings; callers treat zero as "not configured" per their needs
// (the agent accepts empty token as "auth disabled locally", the
// fleet-worker errors on empty gitcache, etc.).
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

	// Pluggable storage URLs (LOCAL-003). Both fields accept the
	// storeurl shapes: fs:///abs/path or s3://bucket/prefix. When set
	// they replace the default filesystem reads in sparkwing-local /
	// `sparkwing dashboard start --on <profile>`.
	LogStore      string `yaml:"log_store,omitempty"`
	ArtifactStore string `yaml:"artifact_store,omitempty"`
}

// Config is the on-disk profiles.yaml file.
type Config struct {
	Default  string              `yaml:"default,omitempty"`
	Profiles map[string]*Profile `yaml:"profiles,omitempty"`
}

// ErrNoProfile is returned by Resolve when no profile can be
// identified. The caller should surface a message telling the user
// to set a default or pass --on.
var ErrNoProfile = errors.New("no profile configured")

// ErrProfileNotFound is returned when --on names a profile that
// doesn't exist in profiles.yaml.
var ErrProfileNotFound = errors.New("profile not found")

// DefaultPath returns the resolved profiles.yaml path for this
// machine, honoring SPARKWING_PROFILES > XDG_CONFIG_HOME > $HOME.
// Missing $HOME is an error (no sane fallback).
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
// an empty Config without error -- callers that need a populated
// file check len(cfg.Profiles) themselves. Parse errors are returned
// so operators see "your profiles.yaml is malformed" rather than
// silent fallback to empty.
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
	// Stamp each profile with its own name so Resolve can return a
	// fully-populated *Profile without a separate lookup.
	for name, p := range cfg.Profiles {
		if p == nil {
			cfg.Profiles[name] = &Profile{Name: name}
		} else {
			p.Name = name
		}
	}
	return &cfg, nil
}

// Save writes cfg to path atomically: marshals, writes a sibling
// tmp file, renames. Creates parent dirs as needed.
//
// Permissions are 0600 because profiles.yaml carries bearer tokens
// in plaintext. A stricter mode makes accidents (cat-into-shared-dir,
// rsync without --chmod) surface as permission errors rather than
// silent credential leaks.
func Save(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	// Copy so we can strip the redundant .Name field before marshal
	// (it duplicates the map key). A fresh map keeps Save's output
	// stable regardless of how callers mutate the in-memory Config.
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
//   - explicitName non-empty -> look up that profile (ErrProfileNotFound
//     if missing).
//   - else if cfg.Default is set -> that profile
//     (ErrProfileNotFound if the default points at an unknown name).
//   - else ErrNoProfile.
//
// The returned pointer is owned by cfg; callers that mutate fields
// should clone first.
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

// LoadAndResolve is the one-shot most client commands want: find
// profiles.yaml on disk, parse it, pick the right profile, return.
// Any step's error is surfaced so the caller can pretty-print it.
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

// Names returns the profile names sorted alphabetically. Used by
// `sparkwing profiles list` and by error messages that want to tell
// the user what's available.
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// HintMissing formats a human-readable error body pointing the
// operator at next steps. Centralized so every command that hits
// "no profile" produces the same message.
func HintMissing(err error, cfg *Config) string {
	base := err.Error()
	if cfg != nil && len(cfg.Profiles) > 0 {
		return fmt.Sprintf("%s\n\nAvailable profiles: %v\nPass --on <name>, or set a default via `sparkwing profiles use <name>`.",
			base, cfg.Names())
	}
	return fmt.Sprintf("%s\n\nRegister a profile first:\n  sparkwing profiles add local --controller http://127.0.0.1:4344 --logs http://127.0.0.1:4345\nOr point at a remote controller:\n  sparkwing profiles add prod --controller https://api.example.dev --token swu_...", base)
}
