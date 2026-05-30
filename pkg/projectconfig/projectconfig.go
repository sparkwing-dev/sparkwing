// Package projectconfig reads the combined .sparkwing/sparkwing.yaml
// project file:
//
//	profile: shared-team
//	pipelines: [ ... ]
//	sources:   { ... }
//	sparks:    [ ... ]
//
// Each section reuses its existing types, and Load normalizes and
// validates them (stamping the map-key Name onto sources, running each
// section's Validate). A missing file returns (nil, nil).
package projectconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// Filename is the combined project-config file name inside .sparkwing/.
const Filename = "sparkwing.yaml"

// migrationGuideURL points adopters at the v0.5.0 config-flatten guide.
const migrationGuideURL = "https://sparkwing.dev/docs/migration-guide/v0.5.0"

// legacyFiles are the pre-v0.5.0 standalone config files that
// sparkwing.yaml absorbed; their presence is now a hard error.
var legacyFiles = []string{
	"pipelines.yaml",
	"backends.yaml",
	"runners.yaml",
	"sources.yaml",
	"sparks.yaml",
}

// CheckLegacy walks up from startDir for the first .sparkwing/ directory
// and errors if it holds any pre-v0.5.0 standalone config file
// (pipelines/backends/runners/sources/sparks.yaml). The error names every
// offending file and points at the migration guide. It returns nil when
// no .sparkwing/ is found or the directory holds only sparkwing.yaml, so
// a fully-migrated repo is silent and a half-migrated one is loud.
func CheckLegacy(startDir string) error {
	dir := startDir
	for {
		sw := filepath.Join(dir, ".sparkwing")
		if info, statErr := os.Stat(sw); statErr == nil && info.IsDir() {
			var present []string
			for _, f := range legacyFiles {
				if _, ferr := os.Stat(filepath.Join(sw, f)); ferr == nil {
					present = append(present, ".sparkwing/"+f)
				}
			}
			if len(present) == 0 {
				return nil
			}
			verb := "is"
			if len(present) > 1 {
				verb = "are"
			}
			return fmt.Errorf("%s %s no longer read in v0.5.0; combine this project's YAML into .sparkwing/%s -- see %s for the layout",
				strings.Join(present, ", "), verb, Filename, migrationGuideURL)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// Config is the parsed .sparkwing/sparkwing.yaml.
type Config struct {
	// Defaults carries the per-pipeline fields each pipeline
	// inherits unless it declares its own. See Defaults for the
	// per-field merge semantics.
	Defaults Defaults `yaml:"defaults,omitempty"`

	// Profiles maps profile name to its surface bundle. The same
	// shape as ~/.config/sparkwing/profiles.yaml's profiles map;
	// project profiles get referenced from inside the project
	// (pipeline.profile, defaults.profile), user profiles from the
	// CLI (--profile).
	Profiles map[string]*profile.Profile `yaml:"profiles,omitempty"`

	Pipelines []pipelines.Pipeline `yaml:"pipelines,omitempty"`
	Sparks    []sparks.Library     `yaml:"sparks,omitempty"`
}

// Defaults is the project-level block of per-pipeline defaults.
type Defaults struct {
	// Profile names the project profile (from Config.Profiles) that
	// applies when neither --profile nor pipeline.profile is set.
	// Empty means "no default" -- a pipeline without its own
	// profile: still runs (against the sqlite-only test/dev shape).
	Profile string `yaml:"profile,omitempty"`

	// Args supplies per-arg default values for every pipeline. Each
	// key is layered under pipeline.args (pipeline wins per-key),
	// and the merged map sits in the priority chain between
	// schema.Computed and the explicit operator CLI flag.
	Args map[string]string `yaml:"args,omitempty"`
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
// ("", nil, nil) when nothing is found anywhere on the walk-up -- the
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

// DiscoverPipelines walks up from startDir for .sparkwing/sparkwing.yaml
// and returns the pipelines section as a *pipelines.Config. It is a
// drop-in for the retired pipelines.Discover: it returns a non-nil error
// when no sparkwing.yaml is found anywhere on the walk-up, and the path
// it returns is the sparkwing.yaml path (so filepath.Dir yields the
// .sparkwing/ directory, same as before).
func DiscoverPipelines(startDir string) (path string, cfg *pipelines.Config, err error) {
	p, pc, err := Discover(startDir)
	if err != nil {
		return "", nil, err
	}
	if p == "" || pc == nil {
		return "", nil, fmt.Errorf("no .sparkwing/%s found from %s up", Filename, startDir)
	}
	return p, &pipelines.Config{Pipelines: pc.Pipelines}, nil
}

// LoadSparksManifest reads the sparks section of
// <sparkwingDir>/sparkwing.yaml as a *sparks.Manifest. Returns (nil, nil)
// when the file is absent or declares no sparks, mirroring the contract
// the retired sparks.LoadManifest had.
func LoadSparksManifest(sparkwingDir string) (*sparks.Manifest, error) {
	if sparkwingDir == "" {
		return nil, errors.New("sparks: sparkwingDir must not be empty")
	}
	cfg, err := Load(filepath.Join(sparkwingDir, Filename))
	if err != nil {
		return nil, err
	}
	if cfg == nil || len(cfg.Sparks) == 0 {
		return nil, nil
	}
	return &sparks.Manifest{Libraries: cfg.Sparks}, nil
}

// WriteSparksSection rewrites the top-level sparks: section of the
// sparkwing.yaml at path to libs, preserving every other section
// (including comments and key order). It creates the file when absent
// and removes the section when libs is empty. This is a surgical
// yaml.Node edit -- it never re-marshals the unrelated sections, so a
// `sparkwing sparks add` can't perturb pipeline/runner/source config.
func WriteSparksSection(path string, libs []sparks.Library) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(raw)) > 0 {
		if uerr := yaml.Unmarshal(raw, &doc); uerr != nil {
			return fmt.Errorf("parse %s: %w", path, uerr)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top-level YAML is not a mapping", path)
	}
	root := doc.Content[0]

	idx := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "sparks" {
			idx = i
			break
		}
	}
	if len(libs) == 0 {
		if idx >= 0 {
			root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		}
	} else {
		var val yaml.Node
		if eerr := val.Encode(libs); eerr != nil {
			return fmt.Errorf("encode sparks: %w", eerr)
		}
		if idx >= 0 {
			root.Content[idx+1] = &val
		} else {
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "sparks"}, &val)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if eerr := enc.Encode(&doc); eerr != nil {
		_ = enc.Close()
		return fmt.Errorf("encode %s: %w", path, eerr)
	}
	if cerr := enc.Close(); cerr != nil {
		return cerr
	}
	if mkerr := os.MkdirAll(filepath.Dir(path), 0o755); mkerr != nil {
		return mkerr
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// normalize validates each section after decode.
func (c *Config) normalize() error {
	pcfg := pipelines.Config{Pipelines: c.Pipelines}
	if err := pcfg.Validate(); err != nil {
		return err
	}

	for name, p := range c.Profiles {
		if p == nil {
			return fmt.Errorf("profile %q: empty body", name)
		}
		p.Name = name
		if err := p.Surfaces().Validate(name); err != nil {
			return err
		}
	}

	if c.Defaults.Profile != "" {
		if _, ok := c.Profiles[c.Defaults.Profile]; !ok {
			return fmt.Errorf("defaults.profile %q is not declared in profiles:", c.Defaults.Profile)
		}
	}

	for _, p := range c.Pipelines {
		if p.Profile != "" {
			if _, ok := c.Profiles[p.Profile]; !ok {
				return fmt.Errorf("pipeline %q: profile %q is not declared in project profiles:", p.Name, p.Profile)
			}
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
