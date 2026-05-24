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
	"reflect"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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

	// AutoAllow pre-authorizes risk labels for this profile. A
	// low-stakes environment (laptop, kind cluster) can declare
	// `auto_allow: [destructive]` so an operator running
	// `sparkwing run destroy-cluster --on laptop` doesn't have to
	// pass `--sw-allow destructive` every time. Production profiles
	// should leave this empty so the gate stays loud.
	AutoAllow []string `yaml:"auto_allow,omitempty"`

	// DefaultRunner names the runner the scheduler picks when a
	// job's Prefers produce no match and more than one runner
	// satisfies its Requires. The name must resolve in runners.yaml
	// at dispatch time -- this layer does no validation against it,
	// because runners.yaml may not exist when a profile is being
	// authored. Empty means "local"; consume via
	// EffectiveDefaultRunner so the fallback lives in one place.
	DefaultRunner string `yaml:"default_runner,omitempty"`

	// State, Cache, and LogsSpec carry the full backend triple so a
	// profile fully describes where its runs persist. Consume them as
	// a unit via Surfaces. A nil pointer means "not specified at this
	// layer"; callers layer the result against project / built-in
	// surfaces with backends.Effective.
	//
	// LogsSpec is tagged logs_backend rather than logs because the
	// legacy Logs field above already owns the logs key as a controller
	// URL string. The two keys coexist until the breaking release folds
	// LogsSpec onto logs and retires the URL field.
	State    *backends.Spec `yaml:"state,omitempty"`
	Cache    *backends.Spec `yaml:"cache,omitempty"`
	LogsSpec *backends.Spec `yaml:"logs_backend,omitempty"`

	// Detect, when set, makes this profile the auto-selected one while
	// its environment condition holds (e.g. GITHUB_ACTIONS=true),
	// ahead of the project hint. The built-in gha and kubernetes
	// profiles ship a Detect block; see BuiltinProfiles.
	Detect *backends.Detect `yaml:"detect,omitempty"`

	// MirrorLocal toggles whether local execution against this profile
	// also writes state to the local SQLite store. Nil means the
	// default (true); set false for automated workers that fire and
	// forget. Consume via EffectiveMirrorLocal.
	MirrorLocal *bool `yaml:"mirror_local,omitempty"`
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
		Logs:  p.LogsSpec,
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

// EffectiveDefaultRunner returns the profile's declared
// default_runner, or "local" when unset. Callers should reach for
// this rather than the raw field so the unset-means-local rule lives
// in one place. Nil-safe: a nil profile resolves to "local" too.
func (p *Profile) EffectiveDefaultRunner() string {
	if p == nil || p.DefaultRunner == "" {
		return "local"
	}
	return p.DefaultRunner
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
			cfg := &Config{Profiles: map[string]*Profile{}}
			mergeBuiltinProfiles(cfg)
			return cfg, nil
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
	mergeBuiltinProfiles(&cfg)
	return &cfg, nil
}

// BuiltinProfiles returns the auto-detect profiles every install gets
// for free: gha (GITHUB_ACTIONS=true) and kubernetes
// (KUBERNETES_SERVICE_HOST present). Load materializes these into the
// returned Config; a user-declared profile of the same name overrides
// the built-in per-field.
func BuiltinProfiles() map[string]*Profile {
	return map[string]*Profile{
		"gha": {
			Name:   "gha",
			Detect: &backends.Detect{EnvVar: "GITHUB_ACTIONS", Equals: "true"},
		},
		"kubernetes": {
			Name:   "kubernetes",
			Detect: &backends.Detect{EnvVar: "KUBERNETES_SERVICE_HOST", Present: true},
		},
	}
}

// mergeBuiltinProfiles layers BuiltinProfiles under cfg.Profiles:
// user-declared values win per field, the built-in fills blanks. A
// name absent from cfg gets the built-in verbatim.
func mergeBuiltinProfiles(cfg *Config) {
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	for name, builtin := range BuiltinProfiles() {
		if user, ok := cfg.Profiles[name]; ok {
			cfg.Profiles[name] = mergeProfile(user, builtin)
		} else {
			cfg.Profiles[name] = builtin
		}
	}
}

// mergeProfile overlays over on top of base per non-zero field, in the
// shape of pkg/backends Merge: over (the user-declared profile) wins,
// base (the built-in) fills blanks.
func mergeProfile(over, base *Profile) *Profile {
	if over == nil {
		return base
	}
	if base == nil {
		return over
	}
	m := *over
	if m.Controller == "" {
		m.Controller = base.Controller
	}
	if m.Logs == "" {
		m.Logs = base.Logs
	}
	if m.Token == "" {
		m.Token = base.Token
	}
	if m.Gitcache == "" {
		m.Gitcache = base.Gitcache
	}
	if m.LogStore == "" {
		m.LogStore = base.LogStore
	}
	if m.ArtifactStore == "" {
		m.ArtifactStore = base.ArtifactStore
	}
	if m.CostPerRunnerHour == 0 {
		m.CostPerRunnerHour = base.CostPerRunnerHour
	}
	if len(m.AutoAllow) == 0 {
		m.AutoAllow = base.AutoAllow
	}
	if m.DefaultRunner == "" {
		m.DefaultRunner = base.DefaultRunner
	}
	if m.State == nil {
		m.State = base.State
	}
	if m.Cache == nil {
		m.Cache = base.Cache
	}
	if m.LogsSpec == nil {
		m.LogsSpec = base.LogsSpec
	}
	m.Detect = mergeDetect(m.Detect, base.Detect)
	if m.MirrorLocal == nil {
		m.MirrorLocal = base.MirrorLocal
	}
	return &m
}

// mergeDetect overlays over on base per non-zero field, matching the
// per-field Detect merge in pkg/backends.
func mergeDetect(over, base *backends.Detect) *backends.Detect {
	if over == nil {
		return base
	}
	if base == nil {
		return over
	}
	m := *over
	if m.EnvVar == "" {
		m.EnvVar = base.EnvVar
	}
	if m.Equals == "" {
		m.Equals = base.Equals
	}
	if !m.Present {
		m.Present = base.Present
	}
	return &m
}

// isBuiltinDefault reports whether p is byte-identical to the built-in
// profile of the given name (ignoring Name). Save uses this to avoid
// persisting the virtual built-ins that Load materialized.
func isBuiltinDefault(name string, p *Profile) bool {
	builtin, ok := BuiltinProfiles()[name]
	if !ok || p == nil {
		return false
	}
	a := *p
	b := *builtin
	a.Name = ""
	b.Name = ""
	return reflect.DeepEqual(a, b)
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
		// Skip the virtual built-ins Load materializes; only persist
		// them once a user has customized one.
		if isBuiltinDefault(name, p) {
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
