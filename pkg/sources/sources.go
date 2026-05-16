// Package sources loads sources.yaml -- the file that names the
// secret/config backends a pipeline target can bind to. Each entry
// describes one backend (a remote controller's vault, the macOS
// keychain, a local dotenv file, or the process environment) and is
// referenced by name from pipelines.yaml's target.source field.
//
// Source precedence (per-field, repo wins):
//
//  1. .sparkwing/sources.yaml         -- team-shared, checked in
//  2. ~/.config/sparkwing/sources.yaml -- per-user additions / overrides
//
// A name in both files merges per non-zero field with repo values
// winning. The file's `default:` key names the source used when a
// pipeline target doesn't bind to a named source explicitly; the
// runtime resolves the default per-call.
//
// Shape (yaml):
//
//	default: team-vault
//	sources:
//	  team-vault:
//	    type: remote-controller
//	    controller: shared        # profile name from profiles.yaml
//	  prod-vault:
//	    type: remote-controller
//	    controller: prod
//	  local-keychain:
//	    type: macos-keychain
//	    service: sparkwing-pi
//	  dotenv:
//	    type: file
//	    path: .sparkwing/secrets.local.env
//	  shell-env:
//	    type: env
//	    prefix: SW_
package sources

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// File is the on-disk shape of sources.yaml.
type File struct {
	// Default names the source used when a target doesn't bind one
	// explicitly. Empty means "no implicit default" -- callers that
	// reach for it should fail with a clear message.
	Default string            `yaml:"default,omitempty"`
	Sources map[string]Source `yaml:"sources,omitempty"`
}

// SourceType discriminator values.
const (
	TypeRemoteController = "remote-controller"
	TypeMacosKeychain    = "macos-keychain"
	TypeFile             = "file"
	TypeEnv              = "env"
)

// Source is one named entry under sources:. Name is populated from
// the map key during Load.
type Source struct {
	Name string `yaml:"-"`

	// Type is the backend kind. Valid values:
	//   "remote-controller" -- HTTPS GET against the named profile's controller
	//   "macos-keychain"    -- /usr/bin/security find-generic-password
	//   "file"              -- dotenv file at Path
	//   "env"               -- os.Getenv with optional Prefix
	Type string `yaml:"type"`

	// Controller is the profile name (from profiles.yaml) hosting the
	// vault for type=remote-controller. Required for that type.
	Controller string `yaml:"controller,omitempty"`

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

// Load reads a single sources.yaml file. A missing file is NOT an
// error; it returns an empty File. Parse errors, unknown fields, and
// validation failures bubble up.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty file is a valid empty File (no sources, no default).
			return File{}, nil
		}
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, s := range f.Sources {
		s.Name = name
		f.Sources[name] = s
	}
	if err := f.Validate(); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Validate checks each source's structural invariants.
func (f *File) Validate() error {
	for name, s := range f.Sources {
		switch s.Type {
		case TypeRemoteController:
			if s.Controller == "" {
				return fmt.Errorf("source %q: type=%s requires a controller field", name, s.Type)
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
				name, TypeRemoteController, TypeMacosKeychain, TypeFile, TypeEnv)
		default:
			return fmt.Errorf("source %q: unknown type %q (valid: %s, %s, %s, %s)",
				name, s.Type, TypeRemoteController, TypeMacosKeychain, TypeFile, TypeEnv)
		}
	}
	if f.Default != "" {
		if _, ok := f.Sources[f.Default]; !ok {
			return fmt.Errorf("default source %q is not declared in sources", f.Default)
		}
	}
	return nil
}

// UserConfigPath returns the per-user sources.yaml location. XDG-aware
// so it sits alongside the rest of sparkwing's user config.
func UserConfigPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "sparkwing", "sources.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "sources.yaml"), nil
}

// RepoConfigPath is .sparkwing/sources.yaml inside the repo.
func RepoConfigPath(sparkwingDir string) string {
	return filepath.Join(sparkwingDir, "sources.yaml")
}

// Resolve loads both files, merges them, and returns the named source.
// Repo values win per non-zero field; user values fill blanks. If
// name is empty, falls back to the repo file's default, then the user
// file's default. Returns (_, true, nil) on success, (_, false, nil)
// when no source matches, and (_, false, err) on parse / IO failures.
func Resolve(sparkwingDir, name string) (Source, bool, error) {
	repoFile, userFile, err := loadBoth(sparkwingDir)
	if err != nil {
		return Source{}, false, err
	}

	if name == "" {
		switch {
		case repoFile.Default != "":
			name = repoFile.Default
		case userFile.Default != "":
			name = userFile.Default
		default:
			return Source{}, false, nil
		}
	}

	repo, inRepo := repoFile.Sources[name]
	user, inUser := userFile.Sources[name]
	if !inRepo && !inUser {
		return Source{}, false, nil
	}

	if inRepo && !inUser {
		repo.Name = name
		return repo, true, nil
	}
	if !inRepo && inUser {
		user.Name = name
		return user, true, nil
	}

	merged := repo
	merged.Name = name
	if merged.Type == "" {
		merged.Type = user.Type
	}
	if merged.Controller == "" {
		merged.Controller = user.Controller
	}
	if merged.Service == "" {
		merged.Service = user.Service
	}
	if merged.Path == "" {
		merged.Path = user.Path
	}
	if merged.Prefix == "" {
		merged.Prefix = user.Prefix
	}
	return merged, true, nil
}

// Names returns every source name visible across both files. Repo
// entries appear before user-only additions. Iteration order within
// a file follows Go's map iteration; callers that need stable order
// should sort.
func Names(sparkwingDir string) ([]string, error) {
	repoFile, userFile, err := loadBoth(sparkwingDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for n := range repoFile.Sources {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for n := range userFile.Sources {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

func loadBoth(sparkwingDir string) (repo, user File, err error) {
	userPath, err := UserConfigPath()
	if err != nil {
		return File{}, File{}, err
	}
	user, err = Load(userPath)
	if err != nil {
		return File{}, File{}, err
	}
	if sparkwingDir != "" {
		repo, err = Load(RepoConfigPath(sparkwingDir))
		if err != nil {
			return File{}, File{}, err
		}
	}
	return repo, user, nil
}
