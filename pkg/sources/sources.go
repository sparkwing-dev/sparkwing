// Package sources defines the secret-source schema carried inline
// under pipelines[].dispatch.source in .sparkwing/sparkwing.yaml.
// Each pipeline declares its own source spec; there is no shared
// registry. A profile may carry a SourceOverride that wholesale
// replaces every pipeline's source at run time (the dev/test escape
// hatch).
package sources

import (
	"fmt"
)

// SourceType discriminator values.
const (
	// TypeController resolves secrets via an HTTPS GET against URL.
	// Auth is the active profile's controller token; the orchestrator
	// requires the source URL to match the active profile's
	// controller URL before passing the token through.
	TypeController = "controller"
	// TypeFile reads secrets from a dotenv file at the given path.
	TypeFile = "file"
	// TypeEnv reads secrets from process env vars, optionally prefixed.
	TypeEnv = "env"
)

// Source is one inline source spec.
type Source struct {
	// Type is the backend kind. Valid values:
	//   "controller" -- HTTPS GET against URL
	//   "file"       -- dotenv file at Path
	//   "env"        -- os.Getenv with optional Prefix
	Type string `yaml:"type"`

	// URL is the controller's base URL for type=controller. Required
	// for that type. The orchestrator enforces that this matches the
	// active profile's controller URL before authenticating.
	URL string `yaml:"url,omitempty"`

	// Path is the dotenv file location for type=file. Required for
	// that type.
	Path string `yaml:"path,omitempty"`

	// Prefix optionally prepended to every name lookup for type=env.
	// Empty means no prefix; the bare secret name is used as the env
	// variable name.
	Prefix string `yaml:"prefix,omitempty"`
}

// Describe returns a short human-readable label for this source,
// suitable for CLI / log output. For type=controller it's
// "controller:URL"; type=file is "file:PATH"; type=env is "env:PREFIX"
// (or just "env").
func (s Source) Describe() string {
	switch s.Type {
	case TypeController:
		return TypeController + ":" + s.URL
	case TypeFile:
		return TypeFile + ":" + s.Path
	case TypeEnv:
		if s.Prefix != "" {
			return TypeEnv + ":" + s.Prefix
		}
		return TypeEnv
	case "":
		return ""
	default:
		return s.Type
	}
}

// Validate checks structural invariants for a single source.
func (s Source) Validate() error {
	switch s.Type {
	case TypeController:
		if s.URL == "" {
			return fmt.Errorf("source type=%s requires a url field", s.Type)
		}
	case TypeFile:
		if s.Path == "" {
			return fmt.Errorf("source type=%s requires a path field", s.Type)
		}
	case TypeEnv:
		// prefix is optional
	case "":
		return fmt.Errorf("source type is required (one of: %s, %s, %s)",
			TypeController, TypeFile, TypeEnv)
	default:
		return fmt.Errorf("source unknown type %q (valid: %s, %s, %s)",
			s.Type, TypeController, TypeFile, TypeEnv)
	}
	return nil
}
