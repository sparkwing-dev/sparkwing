// Package wingconfig loads named flag presets for `wing <pipeline>
// --config <preset>`. Presets bundle wing-owned flags (on, from) so
// operators can stash common combinations instead of retyping.
//
// Source precedence (first match wins, with per-preset field merge
// documented inline):
//
//  1. .sparkwing/config.yaml   -- repo-level, checked in with the repo
//  2. ~/.config/sparkwing/config.yaml  -- user-global
//
// A preset present in both files is merged with repo values winning
// per field (so a personal config can't override team-shared
// defaults). Presets only in the user file resolve as normal.
//
// Shape (yaml):
//
//	configs:
//	  dev:
//	    on: dev-cluster
//	    from: main
//	  prod-deploy:
//	    on: prod
//	    from: main
//
// Typed per-pipeline flags are deliberately absent from this file --
// they land once the pipeline-describe cache is restored (separate
// session). Until then, configs are a pure wing-flag surface.
package wingconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Preset is one entry under configs:. All fields are optional; an
// empty preset just names the default behavior.
type Preset struct {
	// On is the profile name to dispatch remotely against. Applied
	// as --on <name>.
	On string `yaml:"on,omitempty"`
	// From is the git ref to compile against. Applied as --from <ref>.
	From string `yaml:"from,omitempty"`
}

// File mirrors the on-disk shape. Kept separate from Preset so we
// can evolve the wire format without breaking callers.
type File struct {
	Configs map[string]Preset `yaml:"configs"`
}

// Load reads a single wing config file. Missing files are NOT an
// error -- they return an empty File. Parse errors bubble up so
// operators see "yaml: line 3: ..." rather than silent fallback.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

// UserConfigPath returns the per-user config file location, honoring
// $XDG_CONFIG_HOME the same way pkg/profile does so both config
// files live side-by-side under the same directory.
func UserConfigPath() (string, error) {
	if env := os.Getenv("XDG_CONFIG_HOME"); env != "" {
		return filepath.Join(env, "sparkwing", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", "sparkwing", "config.yaml"), nil
}

// RepoConfigPath is .sparkwing/config.yaml inside the repo. Callers
// typically feed this the same path they discover via
// findSparkwingDir -- so sparkwingDir + "/config.yaml".
func RepoConfigPath(sparkwingDir string) string {
	return filepath.Join(sparkwingDir, "config.yaml")
}

// Resolve loads both files, merges them, and looks up `name`. Repo
// wins per field (so team-shared defaults can't be overridden by a
// personal file), but user-only fields fill in gaps -- if the repo
// preset sets only --on and the user preset adds an env, the
// resolved preset has both.
//
// Returns (preset, true, nil) on success, (_, false, nil) when the
// name is absent from both files, and (_, false, err) only on
// parse/IO failures.
func Resolve(sparkwingDir, name string) (Preset, bool, error) {
	userPath, err := UserConfigPath()
	if err != nil {
		return Preset{}, false, err
	}
	userFile, err := Load(userPath)
	if err != nil {
		return Preset{}, false, err
	}

	var repoFile File
	if sparkwingDir != "" {
		repoFile, err = Load(RepoConfigPath(sparkwingDir))
		if err != nil {
			return Preset{}, false, err
		}
	}

	repo, inRepo := repoFile.Configs[name]
	user, inUser := userFile.Configs[name]
	if !inRepo && !inUser {
		return Preset{}, false, nil
	}

	// Merge: repo wins per field, user fills blanks.
	merged := repo
	if !inRepo {
		merged = user
	} else if inUser {
		if merged.On == "" {
			merged.On = user.On
		}
		if merged.From == "" {
			merged.From = user.From
		}
	}
	return merged, true, nil
}

// Names returns every preset name visible from either file, sorted
// in insertion order within each file (repo first, then user
// additions). Used by tab completion to offer --config <TAB>.
func Names(sparkwingDir string) ([]string, error) {
	userPath, err := UserConfigPath()
	if err != nil {
		return nil, err
	}
	userFile, err := Load(userPath)
	if err != nil {
		return nil, err
	}
	var repoFile File
	if sparkwingDir != "" {
		repoFile, err = Load(RepoConfigPath(sparkwingDir))
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	var out []string
	for n := range repoFile.Configs {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for n := range userFile.Configs {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}
