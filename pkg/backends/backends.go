// Package backends defines the typed backend specs (state, cache, logs)
// a storage profile resolves to, plus the per-profile environment-detect
// predicate. The specs are consumed by the storeurl factories
// ([pkg/storage/storeurl]) to open concrete stores and by
// [internal/profile] to describe where a profile's runs persist.
package backends

import "os"

// Surfaces groups the three persistence destinations. A nil pointer
// means "not overridden at this layer."
type Surfaces struct {
	Cache *Spec `yaml:"cache,omitempty"`
	Logs  *Spec `yaml:"logs,omitempty"`
	State *Spec `yaml:"state,omitempty"`
}

// Spec is one backend declaration. Type is the discriminator; the
// remaining fields are interpreted per-type by the storeurl factories.
type Spec struct {
	Type string `yaml:"type"`

	Bucket    string `yaml:"bucket,omitempty"`
	Prefix    string `yaml:"prefix,omitempty"`
	Path      string `yaml:"path,omitempty"`
	URL       string `yaml:"url,omitempty"`
	URLSource string `yaml:"url_source,omitempty"`
	Token     string `yaml:"token,omitempty"`

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
)

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
// surface in over wins, otherwise base's surface is kept. Used to layer
// a pipeline's per-target backend overrides on top of the resolved
// profile surfaces.
func LayerSurfaces(base, over Surfaces) Surfaces {
	return Surfaces{
		Cache: layerSpec(base.Cache, over.Cache),
		Logs:  layerSpec(base.Logs, over.Logs),
		State: layerSpec(base.State, over.State),
	}
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
	if merged.Binaries == nil {
		merged.Binaries = base.Binaries
	}
	return &merged
}
