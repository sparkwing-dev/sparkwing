// Package projectconfig reads the combined .sparkwing/sparkwing.yaml
// project file. It aggregates, under top-level section keys, the
// content that previously lived in the separate pipelines.yaml,
// runners.yaml, sources.yaml, and sparks.yaml files, plus an optional
// profile hint:
//
//	profile: shared-team
//	pipelines: [ ... ]   # the list pipelines.yaml carried under pipelines:
//	runners:   { ... }   # the map runners.yaml carried under runners:
//	sources:   { ... }   # the whole sources.yaml File (default: + sources:)
//	sparks:    [ ... ]   # the list sparks.yaml carried under libraries:
//
// Each section reuses the existing per-file types, and Load normalizes
// and validates them exactly as the per-file loaders do (stamping the
// map-key Name onto runners and sources, dropping empty runner labels,
// running each section's Validate). Consumers can therefore switch to
// this reader without changing how they read the resulting structs.
//
// Load is deliberately strict about its single file: it does not fall
// back to the legacy per-file YAMLs when sparkwing.yaml is absent. That
// fallback is the caller's responsibility during the transitional
// window; a missing file simply returns (nil, nil).
package projectconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

// Filename is the combined project-config file name inside .sparkwing/.
const Filename = "sparkwing.yaml"

// Config is the parsed .sparkwing/sparkwing.yaml. Each section mirrors
// the shape of the per-file YAML it absorbs; see the package doc.
type Config struct {
	// Profile is an optional hint naming which profile this repo
	// expects when no --profile flag is passed. It is advisory only;
	// resolution layers it below the explicit flag.
	Profile string `yaml:"profile,omitempty"`

	Pipelines []pipelines.Pipeline      `yaml:"pipelines,omitempty"`
	Runners   map[string]runners.Runner `yaml:"runners,omitempty"`
	Sources   *sources.File             `yaml:"sources,omitempty"`
	Sparks    []sparks.Library          `yaml:"sparks,omitempty"`
}

// Load reads and parses .sparkwing/sparkwing.yaml at path. A missing
// file returns (nil, nil) so the caller can fall back to the legacy
// per-file loaders. Unknown fields at any level fail the parse with the
// file path in the message. Each section is normalized and validated to
// match its per-file loader.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// Discover walks up from startDir looking for
// <dir>/.sparkwing/sparkwing.yaml, stopping at the filesystem root. It
// returns the absolute path and loaded Config of the first match, or
// ("", nil, nil) when nothing is found anywhere on the walk-up — the
// caller falls back to the legacy per-file discovery.
func Discover(startDir string) (path string, cfg *Config, err error) {
	dir := startDir
	for {
		candidate := filepath.Join(dir, ".sparkwing", Filename)
		if _, statErr := os.Stat(candidate); statErr == nil {
			loaded, lerr := Load(candidate)
			if lerr != nil {
				return candidate, nil, lerr
			}
			return candidate, loaded, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, nil
		}
		dir = parent
	}
}

// normalize applies the same per-section post-decode work the
// individual loaders do, so Config is byte-for-byte equivalent to what
// pipelines.Parse / runners.Load / sources.Load / sparks.LoadManifest
// would produce on the same content.
func (c *Config) normalize() error {
	pcfg := pipelines.Config{Pipelines: c.Pipelines}
	if err := pcfg.Validate(); err != nil {
		return err
	}

	for name, r := range c.Runners {
		r.Name = name
		r.Labels = filterEmpty(r.Labels)
		c.Runners[name] = r
	}
	rfile := runners.File{Runners: c.Runners}
	if err := rfile.Validate(); err != nil {
		return err
	}

	if c.Sources != nil {
		for name, s := range c.Sources.Sources {
			s.Name = name
			c.Sources.Sources[name] = s
		}
		if err := c.Sources.Validate(); err != nil {
			return err
		}
	}

	for i, lib := range c.Sparks {
		if lib.Source == "" {
			return fmt.Errorf("sparks: entry %d missing 'source'", i)
		}
		if lib.Version == "" {
			return fmt.Errorf("sparks: entry %d (%s) missing 'version'", i, lib.Source)
		}
	}
	return nil
}

// filterEmpty drops empty strings from a label slice, matching
// runners.Load's normalization so a runner parsed here equals one
// parsed from a standalone runners.yaml.
func filterEmpty(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
