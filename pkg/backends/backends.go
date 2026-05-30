// Package backends defines the typed backend specs (state, cache, logs)
// a storage profile resolves to, plus the per-profile environment-detect
// predicate. The specs are consumed by the storeurl factories
// ([pkg/storage/storeurl]) to open concrete stores and by
// [internal/profile] to describe where a profile's runs persist.
package backends

import (
	"fmt"
	"os"
)

// Surfaces groups the four persistence destinations a run touches.
// A nil pointer means "not declared at this layer."
type Surfaces struct {
	Secrets *Spec `yaml:"secrets,omitempty"`
	Cache   *Spec `yaml:"cache,omitempty"`
	Logs    *Spec `yaml:"logs,omitempty"`
	State   *Spec `yaml:"state,omitempty"`
}

// Spec is one backend declaration. Type is the discriminator; the
// remaining fields are interpreted per-type by the storeurl factories
// (state/cache/logs) and the secrets resolver (secrets).
type Spec struct {
	Type string `yaml:"type"`

	Bucket    string `yaml:"bucket,omitempty"`
	Prefix    string `yaml:"prefix,omitempty"`
	Path      string `yaml:"path,omitempty"`
	URL       string `yaml:"url,omitempty"`
	URLSource string `yaml:"url_source,omitempty"`
	Token     string `yaml:"token,omitempty"`

	// TokenEnv names an env var the runtime reads to populate Token
	// at use time. Set TokenEnv on checked-in config so the token
	// itself stays out of git; leave Token empty when TokenEnv is
	// set. The env-var read happens lazily at the call site that
	// needs the token (secrets resolver, controller storeurl).
	TokenEnv string `yaml:"token_env,omitempty"`

	// Controller names a profile from profiles.yaml for type=controller
	// backends. The orchestrator resolves the name to a controller URL
	// and bearer token via the same profile-lookup callback used by
	// profile secret sources.
	Controller string `yaml:"controller,omitempty"`

	// Binaries is an optional nested override on Cache that isolates
	// compiled pipeline binaries to a separate destination (e.g.
	// shared s3 bucket while the rest of cache stays on disk). Only
	// valid on the cache surface.
	Binaries *Spec `yaml:"binaries,omitempty"`
}

// Backend type discriminators.
const (
	TypeFilesystem = "filesystem"
	TypeS3         = "s3"
	TypeGCS        = "gcs"
	TypeAzureBlob  = "azure-blob"
	TypeController = "controller"
	TypeStdout     = "stdout"
	TypeSQLite     = "sqlite"
	TypePostgres   = "postgres"
	TypeMySQL      = "mysql"
	TypeEnv        = "env" // env vars; valid only on the secrets surface
)

// ResolvedToken returns Token directly when it's set, else reads
// TokenEnv from the process environment. Empty string when neither is
// configured -- callers that need auth should error explicitly on
// empty rather than relying on ResolvedToken to flag it.
func (s *Spec) ResolvedToken() string {
	if s == nil {
		return ""
	}
	if s.Token != "" {
		return s.Token
	}
	if s.TokenEnv != "" {
		return envOr(s.TokenEnv, "")
	}
	return ""
}

func envOr(name, fallback string) string {
	if v, ok := lookupEnv(name); ok {
		return v
	}
	return fallback
}

// lookupEnv is a thin wrapper kept for test injection if ever needed.
var lookupEnv = os.LookupEnv

// Detect describes when a profile auto-selects against the current
// environment.
//
// EnvVar names the variable to consult. Equals matches a specific
// value; Present matches any non-empty value. Setting neither never
// matches.
type Detect struct {
	EnvVar  string `yaml:"env_var,omitempty"`
	Equals  string `yaml:"equals,omitempty"`
	Present bool   `yaml:"present,omitempty"`
}

// Match reports whether this detect rule evaluates true against the
// current process environment. A zero EnvVar never matches; Equals
// requires the variable to equal that value; Present requires it to be
// set and non-empty. This is the canonical detect predicate used by
// profile resolution.
func (d Detect) Match() bool {
	if d.EnvVar == "" {
		return false
	}
	v, ok := os.LookupEnv(d.EnvVar)
	if !ok {
		return false
	}
	if d.Equals != "" {
		return v == d.Equals
	}
	if d.Present {
		return v != ""
	}
	return false
}

// LayerSurfaces overlays over on top of base per surface: a non-nil
// surface in over wins, otherwise base's surface is kept. Used to
// layer overrides on top of project defaults.
func LayerSurfaces(base, over Surfaces) Surfaces {
	return Surfaces{
		Secrets: layerSpec(base.Secrets, over.Secrets),
		Cache:   layerSpec(base.Cache, over.Cache),
		Logs:    layerSpec(base.Logs, over.Logs),
		State:   layerSpec(base.State, over.State),
	}
}

// ValidateSecrets enforces the type vocabulary for the secrets
// surface: only controller / filesystem / env make sense (state
// backends like sqlite are nonsensical for secret lookup). Returns
// nil for an absent spec.
func (s *Spec) ValidateSecrets() error {
	if s == nil {
		return nil
	}
	switch s.Type {
	case TypeController:
		if s.URL == "" {
			return secretsErr("type=controller requires url")
		}
	case TypeFilesystem:
		if s.Path == "" {
			return secretsErr("type=filesystem requires path")
		}
	case TypeEnv:
		// prefix optional
	case "":
		return secretsErr("type is required (controller | filesystem | env)")
	default:
		return secretsErr("unsupported type %q (controller | filesystem | env)", s.Type)
	}
	return nil
}

func secretsErr(format string, a ...any) error {
	return fmt.Errorf("secrets backend: "+format, a...)
}

// layerSpec overlays over on top of base per non-zero field. A
// different over.Type takes everything from over and ignores base
// (a kind change resets the spec).
func layerSpec(base, over *Spec) *Spec {
	if over == nil {
		return base
	}
	if base == nil || base.Type != over.Type {
		clone := *over
		return &clone
	}
	merged := *over
	if merged.Bucket == "" {
		merged.Bucket = base.Bucket
	}
	if merged.Prefix == "" {
		merged.Prefix = base.Prefix
	}
	if merged.Path == "" {
		merged.Path = base.Path
	}
	if merged.URL == "" {
		merged.URL = base.URL
	}
	if merged.URLSource == "" {
		merged.URLSource = base.URLSource
	}
	if merged.Token == "" {
		merged.Token = base.Token
	}
	if merged.TokenEnv == "" {
		merged.TokenEnv = base.TokenEnv
	}
	if merged.Binaries == nil {
		merged.Binaries = base.Binaries
	}
	return &merged
}
