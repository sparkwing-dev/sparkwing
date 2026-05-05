// Package sparks resolves sparks library dependencies declared in a
// consumer repo's .sparkwing/sparks.yaml and materializes an overlay
// modfile (.sparkwing/.resolved.mod + .resolved.sum) used by the compile
// step via `go build -modfile=...`.
//
// The consumer's git-tracked go.mod and go.sum are never modified by this
// package. Callers feed it the consumer sparkwing dir (the directory that
// contains go.mod + pipelines.yaml + main.go) and receive an overlay that
// the compile step can point at.
//
// See regression_fixes.md REG-011b for the full design.
package sparks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// ManifestFilename is the name of the consumer manifest inside the
// sparkwing dir.
const ManifestFilename = "sparks.yaml"

// Manifest is the parsed .sparkwing/sparks.yaml.
type Manifest struct {
	Libraries []Library `yaml:"libraries"`
}

// Library is one entry in the consumer manifest.
type Library struct {
	// Name is the logical name; matches spark.json:name. Advisory only
	// for the resolver; Source drives module resolution.
	Name string `yaml:"name"`
	// Source is the Go module path (e.g. github.com/sparkwing-dev/sparks-core).
	Source string `yaml:"source"`
	// Version is "latest", a semver range ("^v0.10.0", "~v0.10.0",
	// ">=v0.10.0") or an exact tag ("v0.10.3").
	Version string `yaml:"version"`
}

// LoadManifest reads .sparkwing/sparks.yaml from a consumer repo's
// sparkwing dir. Returns (nil, nil) if the file is absent - no sparks.yaml
// means no resolution is needed and the caller takes the fast path.
func LoadManifest(sparkwingDir string) (*Manifest, error) {
	if sparkwingDir == "" {
		return nil, errors.New("sparks: sparkwingDir must not be empty")
	}
	path := filepath.Join(sparkwingDir, ManifestFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sparks: reading %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sparks: parsing %s: %w", path, err)
	}
	for i, lib := range m.Libraries {
		if lib.Source == "" {
			return nil, fmt.Errorf("sparks: %s: entry %d missing 'source'", path, i)
		}
		if lib.Version == "" {
			return nil, fmt.Errorf("sparks: %s: entry %d (%s) missing 'version'", path, i, lib.Source)
		}
	}
	return &m, nil
}
