// Package sources defines the secret-source schema carried under the
// sources: section of .sparkwing/sparkwing.yaml. It owns the types and
// their validation; the file is read by pkg/projectconfig, which
// normalizes and validates each section.
package sources

import (
	"fmt"
)

// File is the shape of the sources: section: an optional default plus
// the named entries.
type File struct {
	// Default names the source used when a target doesn't bind one
	// explicitly. Empty means "no implicit default" -- callers that
	// reach for it should fail with a clear message.
	Default string `yaml:"default,omitempty"`
	// Sources is the map of named entries. It is keyed `entries:` under
	// the sparkwing.yaml sources: section (so the section reads
	// `sources: { default:, entries: {...} }`).
	Sources map[string]Source `yaml:"entries,omitempty"`
}

// SourceType discriminator values.
const (
	// TypeProfile resolves secrets via an HTTPS GET against the named
	// profile's controller. (Renamed from "remote-controller" in
	// v0.5.0 -- it always pointed at a profile.)
	TypeProfile       = "profile"
	TypeMacosKeychain = "macos-keychain"
	TypeFile          = "file"
	TypeEnv           = "env"
)

// Source is one named entry under sources:. Name is populated from
// the map key during load.
type Source struct {
	Name string `yaml:"-"`

	// Type is the backend kind. Valid values:
	//   "profile"        -- HTTPS GET against the named profile's controller
	//   "macos-keychain" -- /usr/bin/security find-generic-password
	//   "file"           -- dotenv file at Path
	//   "env"            -- os.Getenv with optional Prefix
	Type string `yaml:"type"`

	// Profile is the profile name (from profiles.yaml) hosting the
	// vault for type=profile. Required for that type. (Renamed from
	// controller: in v0.5.0.)
	Profile string `yaml:"profile,omitempty"`

	// Service is the macOS keychain service name for
	// type=macos-keychain. Required for that type.
	Service string `yaml:"service,omitempty"`

	// Path is the dotenv file location for type=file. Required for
	// that type.
	Path string `yaml:"path,omitempty"`

	// Prefix optionally prepended to every name lookup for type=env.
	// Empty means no prefix; the bare secret name is used as the env
	// variable name.
	Prefix string `yaml:"prefix,omitempty"`
}

// Validate checks each source's structural invariants.
func (f *File) Validate() error {
	for name, s := range f.Sources {
		switch s.Type {
		case TypeProfile:
			if s.Profile == "" {
				return fmt.Errorf("source %q: type=%s requires a profile field", name, s.Type)
			}
		case TypeMacosKeychain:
			if s.Service == "" {
				return fmt.Errorf("source %q: type=%s requires a service field", name, s.Type)
			}
		case TypeFile:
			if s.Path == "" {
				return fmt.Errorf("source %q: type=%s requires a path field", name, s.Type)
			}
		case TypeEnv:
			// prefix is optional
		case "":
			return fmt.Errorf("source %q: type is required (one of: %s, %s, %s, %s)",
				name, TypeProfile, TypeMacosKeychain, TypeFile, TypeEnv)
		default:
			return fmt.Errorf("source %q: unknown type %q (valid: %s, %s, %s, %s)",
				name, s.Type, TypeProfile, TypeMacosKeychain, TypeFile, TypeEnv)
		}
	}
	if f.Default != "" {
		if _, ok := f.Sources[f.Default]; !ok {
			return fmt.Errorf("default source %q is not declared in sources", f.Default)
		}
	}
	return nil
}
