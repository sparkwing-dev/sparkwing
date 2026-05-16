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
	// Secrets is the declared secrets surface. Each entry is either a
	// bare string (legacy form, treated as required) or a typed
	// SecretEntry with explicit required/optional. See SecretsField.
	Secrets SecretsField `yaml:"secrets,omitempty"`
	Tags    []string     `yaml:"tags,omitempty"`
	// Hidden omits the entry from default `wing <TAB>` listings.
	// It is still invocable by typing the exact name. Used for
	// rarely-used tools (demos, scaffolding, one-shot utilities)
	// that would otherwise clutter the completion menu.
	Hidden bool `yaml:"hidden,omitempty"`
	// Group is the section header this entry appears under in
	// `wing <TAB>`. Free-form (e.g. "CI", "Release", "Build"). When
	// empty, falls back to "Pipelines" for triggered entries and
	// "Commands" for manual-only entries.
	Group string `yaml:"group,omitempty"`

	// Runners is the pipeline-level runner allowlist. Jobs default
	// to this set; per-target Runners narrows it (intersection);
	// per-job Requires narrows further. Empty means "any runner
	// allowed."
	Runners []string `yaml:"runners,omitempty"`

	// Targets enumerates the named environments this pipeline can
	// act on. Zero targets means the pipeline has no target concept
	// and the CLI rejects --for; one target auto-selects; two or
	// more require --for to disambiguate.
	Targets map[string]Target `yaml:"targets,omitempty"`

	// Values is the layered config-value surface for this pipeline.
	// See PipelineValues for the layering rule.
	Values PipelineValues `yaml:"values,omitempty"`
}

// Target is one named environment a pipeline can act on. Every field
// is optional; omitted fields inherit from the pipeline level.
type Target struct {
	// Runners narrows the pipeline-level runner allowlist for runs
	// against this target. Intersection with Pipeline.Runners. Empty
	// means inherit the pipeline allowlist.
	Runners []string `yaml:"runners,omitempty"`

	// Source names an entry in sources.yaml that resolves Secret /
	// Config calls for runs against this target. Empty means fall
	// back to the pipeline's default source (or the global default
	// declared in sources.yaml).
	Source string `yaml:"source,omitempty"`

	// Approvals, if non-empty, gates dispatch on a human response
	// before any jobs run. Today only "required" is accepted.
	Approvals string `yaml:"approvals,omitempty"`

	// Protected refuses non-default-branch sources and surfaces a
	// loud banner in the dashboard. Use on production-targeting
	// entries to keep an ad-hoc branch run from reaching real infra.
	Protected bool `yaml:"protected,omitempty"`

	// Values overlays onto the pipeline's typed config struct for
	// runs against this target. Keys are config field tags; values
	// are typed by the pipeline's Config struct at consumption.
	Values map[string]any `yaml:"values,omitempty"`

	// Backend overrides cache / logs / state destinations for runs
	// against this target. Per-surface shape is intentionally left
	// as map[string]any here; the typed BackendSpec lands with
	// backends.yaml in a later step.
	Backend *TargetBackend `yaml:"backend,omitempty"`
}

// PipelineValues is the layered config-value surface declared on a
// pipeline. Base applies to every run; Runners is a per-runner
// overlay keyed by the runner name (matching runners.yaml). The
// per-target Values overlay (Target.Values) sits between these two:
// Base < Target.Values < Runners[chosen-runner].
type PipelineValues struct {
	// Base values applied to every run regardless of target or
	// runner. Equivalent to a "default" key in earlier prototypes.
	Base map[string]any `yaml:"base,omitempty"`

	// Runners is a per-runner overlay, applied after the target's
	// Values. Key is the runner name from runners.yaml.
	Runners map[string]map[string]any `yaml:"runners,omitempty"`
}

// TargetBackend carries per-surface backend overrides for runs
// against a target. Each surface stays untyped (map[string]any) so
// callers can declare any shape backends.yaml will support without
// requiring a parser update here.
type TargetBackend struct {
	Cache map[string]any `yaml:"cache,omitempty"`
	Logs  map[string]any `yaml:"logs,omitempty"`
	State map[string]any `yaml:"state,omitempty"`
}

// SecretEntry is one typed secret declaration. Name names the secret
// in the pipeline's Secrets struct (and in the source backing it).
// Required and Optional are mutually exclusive; the validator
// enforces that. Neither set defaults to Required=true at parse time
// to match the bare-string legacy semantics.
type SecretEntry struct {
	Name     string `yaml:"name" json:"name"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Optional bool   `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// SecretsField is the polymorphic shape of the secrets: list. The
// custom UnmarshalYAML accepts each element as either a bare string
// (legacy) or a typed mapping (new). Mixed lists are allowed.
//
//	secrets: [FOO, BAR]                            # legacy: both required
//	secrets: [{name: FOO, required: true}, ...]    # typed
//	secrets: [BAR, {name: FOO, optional: true}]    # mixed
//
// Bare strings map to SecretEntry{Name: s, Required: true} -- the
// fail-fast posture is the closer parallel to the legacy behavior of
// "declared up front for documentation," now that the resolver
// actually enforces it.
type SecretsField []SecretEntry

// UnmarshalYAML implements yaml.Unmarshaler. The list must be a
// sequence node; each element is dispatched by Kind: scalar nodes
// become bare-string entries (required by default), mapping nodes
// decode into a SecretEntry struct.
func (s *SecretsField) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias != nil {
			return s.UnmarshalYAML(node.Alias)
		}
		return nil
	case yaml.SequenceNode:
		// proceed
	case 0:
		return nil
	default:
		return fmt.Errorf("secrets: expected a sequence, got %s", nodeKindName(node.Kind))
	}
	out := make(SecretsField, 0, len(node.Content))
	for i, elem := range node.Content {
		switch elem.Kind {
		case yaml.ScalarNode:
			var name string
			if err := elem.Decode(&name); err != nil {
				return fmt.Errorf("secrets[%d]: %w", i, err)
			}
			out = append(out, SecretEntry{Name: name, Required: true})
		case yaml.MappingNode:
			var entry SecretEntry
			if err := elem.Decode(&entry); err != nil {
				return fmt.Errorf("secrets[%d]: %w", i, err)
			}
			out = append(out, entry)
		default:
			return fmt.Errorf("secrets[%d]: expected a string or mapping, got %s", i, nodeKindName(elem.Kind))
		}
	}
	*s = out
	return nil
}

// MarshalYAML emits the typed form. Round-tripping a parsed
// SecretsField produces a normalized list of mappings rather than
// the legacy bare-string shape; callers that need to preserve the
// original literal form should keep the raw yaml bytes around.
func (s SecretsField) MarshalYAML() (any, error) {
	if len(s) == 0 {
		return nil, nil
	}
	out := make([]SecretEntry, len(s))
	copy(out, s)
	return out, nil
}

func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return fmt.Sprintf("kind=%d", k)
	}
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

		if err := p.Secrets.Validate(p.Name); err != nil {
			return err
		}
		for tname, t := range p.Targets {
			if err := t.Validate(p.Name, tname); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks one Target's structural invariants. Today only the
// Approvals value is constrained; future approvals types ("two-person",
// etc.) extend the accepted set.
func (t Target) Validate(pipeline, name string) error {
	switch t.Approvals {
	case "", "required":
		// ok
	default:
		return fmt.Errorf("pipeline %q target %q: approvals = %q is not a recognized value (accepted: required)", pipeline, name, t.Approvals)
	}
	return nil
}

// Validate checks every secret entry under one pipeline.
func (s SecretsField) Validate(pipeline string) error {
	for i, e := range s {
		if e.Name == "" {
			return fmt.Errorf("pipeline %q secrets[%d]: name is required", pipeline, i)
		}
		if e.Required && e.Optional {
			return fmt.Errorf("pipeline %q secret %q: required and optional are mutually exclusive", pipeline, e.Name)
		}
	}
	return nil
}

// IsRequired reports whether the entry is treated as required at run
// start. Defaults to true when neither field is set, matching the
// fail-fast posture of the bare-string legacy form.
func (e SecretEntry) IsRequired() bool {
	if e.Optional {
		return false
	}
	return true
}

// TargetNames returns the pipeline's declared target names in sorted
// order. Empty when no targets are declared.
func (p *Pipeline) TargetNames() []string {
	if len(p.Targets) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Targets))
	for name := range p.Targets {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// HasTarget reports whether the pipeline declares a target with the
// given name.
func (p *Pipeline) HasTarget(name string) bool {
	_, ok := p.Targets[name]
	return ok
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
