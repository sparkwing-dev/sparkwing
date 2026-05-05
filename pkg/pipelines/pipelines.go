// Package pipelines parses .sparkwing/pipelines.yaml, the registry
// that maps each pipeline's invocation name to the Go type that
// implements it plus its trigger rules, declared secrets, and tags.
//
// The file is intentionally a thin registry. The Plan itself, jobs,
// conditions, and per-step details all live in Go code; pipelines.yaml
// only holds metadata the controller needs before loading Go.
package pipelines

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the whole pipelines.yaml contents.
type Config struct {
	Pipelines []Pipeline `yaml:"pipelines"`
}

// Pipeline is one registry entry.
type Pipeline struct {
	Name       string   `yaml:"name"`
	Entrypoint string   `yaml:"entrypoint"`
	On         Triggers `yaml:"on"`
	// Secrets is preserved for backward-compat parsing of existing
	// pipelines.yaml files that still list a `secrets:` field. The
	// REG-017 lazy resolver makes this declaration unnecessary --
	// jobs call sparkwing.Secret(ctx, name) on demand. Treated as a
	// pure no-op; new pipelines should omit the field.
	Secrets []string `yaml:"secrets"`
	Tags    []string `yaml:"tags"`
	// Hidden omits the entry from default `wing <TAB>` listings.
	// It is still invocable by typing the exact name. Used for
	// rarely-used tools (demos, scaffolding, one-shot utilities)
	// that would otherwise clutter the completion menu.
	Hidden bool `yaml:"hidden"`
	// Group is the section header this entry appears under in
	// `wing <TAB>`. Free-form (e.g. "CI", "Release", "Build"). When
	// empty, falls back to "Pipelines" for triggered entries and
	// "Commands" for manual-only entries.
	Group string `yaml:"group"`
}

// Triggers groups the declared trigger rules. All fields are optional;
// a pipeline with no triggers can still be invoked manually via
// `wing <name>`.
type Triggers struct {
	Manual   *ManualTrigger   `yaml:"manual,omitempty"`
	Push     *PushTrigger     `yaml:"push,omitempty"`
	Schedule string           `yaml:"schedule,omitempty"`
	Webhook  *WebhookTrigger  `yaml:"webhook,omitempty"`
	Deploy   *DeployTrigger   `yaml:"deploy,omitempty"`
	PreHook  *PreHookTrigger  `yaml:"pre_commit,omitempty"`
	PostHook *PostHookTrigger `yaml:"pre_push,omitempty"`
}

// ManualTrigger is the explicit opt-in for `wing <name>`. Pipelines
// without any trigger declared are still manually invocable; this
// exists so authors can tag a pipeline as manual-only for clarity.
type ManualTrigger struct{}

// PushTrigger fires on git push events matching the rules.
type PushTrigger struct {
	Branches []string          `yaml:"branches"`
	Paths    []string          `yaml:"paths"`
	Env      map[string]string `yaml:"env"`
}

// WebhookTrigger exposes an HTTP path that fires the pipeline. The
// controller assembles a RunContext from the incoming request.
type WebhookTrigger struct {
	Path string `yaml:"path"`
}

// DeployTrigger is the implicit trigger for deployment pipelines that
// want to run when another pipeline reports a deployable artifact.
// Kept as a typed placeholder until cluster mode lands.
type DeployTrigger struct{}

// PreHookTrigger fires from a pre-commit git hook. Scoped to fast
// local checks.
type PreHookTrigger struct{}

// PostHookTrigger fires from a pre-push git hook. Scoped to heavier
// checks like full test suites.
type PostHookTrigger struct{}

// Load reads and parses the pipelines.yaml at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse decodes a pipelines.yaml from r.
func Parse(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if err == io.EOF {
			return &cfg, nil
		}
		return nil, fmt.Errorf("parse pipelines.yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate returns an error describing any structural problem in the
// config. Callers typically surface these as pipeline-definition
// errors and halt.
func (c *Config) Validate() error {
	seen := map[string]struct{}{}
	for i, p := range c.Pipelines {
		if p.Name == "" {
			return fmt.Errorf("pipelines[%d]: name is required", i)
		}
		if p.Entrypoint == "" {
			return fmt.Errorf("pipeline %q: entrypoint is required", p.Name)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("pipeline %q: duplicate name", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// Find returns the pipeline with the given name, or nil if absent.
func (c *Config) Find(name string) *Pipeline {
	for i := range c.Pipelines {
		if c.Pipelines[i].Name == name {
			return &c.Pipelines[i]
		}
	}
	return nil
}

// Names returns the declared pipeline names, preserving file order.
func (c *Config) Names() []string {
	out := make([]string, 0, len(c.Pipelines))
	for _, p := range c.Pipelines {
		out = append(out, p.Name)
	}
	return out
}

// Discover walks up from startDir looking for a .sparkwing/pipelines.yaml.
// Returns the absolute path and its loaded Config.
func Discover(startDir string) (path string, cfg *Config, err error) {
	dir := startDir
	for {
		candidate := filepath.Join(dir, ".sparkwing", "pipelines.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			loaded, lerr := Load(candidate)
			if lerr != nil {
				return candidate, nil, lerr
			}
			return candidate, loaded, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, fmt.Errorf("no .sparkwing/pipelines.yaml found from %s up", startDir)
		}
		dir = parent
	}
}

// EntrypointsByName returns a map of pipeline name -> entrypoint type
// name. Convenient for matching against sparkwing.TypeName of
// registered instances.
func (c *Config) EntrypointsByName() map[string]string {
	out := make(map[string]string, len(c.Pipelines))
	for _, p := range c.Pipelines {
		out[p.Name] = p.Entrypoint
	}
	return out
}

// Equal reports whether two configs describe the same pipeline set.
// Order-insensitive over the top-level pipelines list. Useful for
// round-trip tests.
func (c *Config) Equal(other *Config) bool {
	if len(c.Pipelines) != len(other.Pipelines) {
		return false
	}
	left := map[string]Pipeline{}
	for _, p := range c.Pipelines {
		left[p.Name] = p
	}
	for _, p := range other.Pipelines {
		lp, ok := left[p.Name]
		if !ok {
			return false
		}
		if lp.Entrypoint != p.Entrypoint {
			return false
		}
		if !strings.EqualFold(joinSorted(lp.Tags), joinSorted(p.Tags)) {
			return false
		}
	}
	return true
}

func joinSorted(s []string) string {
	cp := append([]string(nil), s...)
	sortStrings(cp)
	return strings.Join(cp, ",")
}

func sortStrings(s []string) {
	// Small list sort to avoid pulling sort just for test helpers.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
