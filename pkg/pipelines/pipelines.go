package pipelines

import (
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// Config is the whole pipelines.yaml contents.
type Config struct {
	Pipelines []Pipeline `yaml:"pipelines"`
}

// Pipeline is one registry entry. A pipeline binds a Go entrypoint
// (declared via [sparkwing.RegisterEntrypoint]) to one named
// deployment shape: defaults, guards, dispatch metadata, triggers,
// secrets surface. One Go entrypoint can back many pipelines, each
// with its own policy.
type Pipeline struct {
	Name        string       `yaml:"name"`
	Entrypoint  string       `yaml:"entrypoint"`
	Description string       `yaml:"description,omitempty"`
	On          Triggers     `yaml:"on,omitempty"`
	Secrets     SecretsField `yaml:"secrets,omitempty"`

	// Guards gate dispatch on the resolved profile + args. Reject
	// fires before any step runs when any token matches; Require
	// fires when not every token matches. Token vocabulary:
	// `profile-local`, `profile-controller`, `profile-name:<name>`,
	// `arg:<flag>=<value>`. See pkg/pipelines/guards.go.
	Guards Guards `yaml:"guards,omitempty"`

	// Args supplies per-arg default values. Higher priority than
	// schema Default and Computed; lower than an explicit operator
	// CLI flag. Keyed by CLI flag name (kebab-case, matching what
	// the SDK's WithArgs[T] field tags resolve to).
	Args map[string]string `yaml:"args,omitempty"`

	// Profile names the project profile (from sparkwing.yaml's
	// profiles map) this pipeline uses. Empty means "fall back to
	// the project's top-level profile: selector". The CLI's
	// --profile flag (which targets ~/.config/sparkwing/profiles.yaml)
	// overrides this when present.
	Profile string `yaml:"profile,omitempty"`
}

// Guards is the pipeline-level dispatch gate. Both fields are lists
// of flat predicate tokens evaluated against the resolved profile +
// args at run start. Require fires (rejecting dispatch) when not
// every token matches; Reject fires when any token matches.
//
// See pkg/pipelines/guards.go for the token vocabulary and
// evaluation rules.
type Guards struct {
	Require []string `yaml:"require,omitempty"`
	Reject  []string `yaml:"reject,omitempty"`
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

// SecretsField is the typed list of secret declarations on a
// pipeline. Each entry is a mapping with name + required/optional:
//
//	secrets:
//	  - {name: DEPLOY_TOKEN, required: true}
//	  - {name: SLACK_HOOK,   optional: true}
type SecretsField []SecretEntry

// UnmarshalYAML implements yaml.Unmarshaler. The list must be a
// sequence of mapping nodes; scalar nodes (the legacy bare-string
// form) produce a clear migration error pointing at the typed
// shape.
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
		case yaml.MappingNode:
			var entry SecretEntry
			if err := elem.Decode(&entry); err != nil {
				return fmt.Errorf("secrets[%d]: %w", i, err)
			}
			out = append(out, entry)
		case yaml.ScalarNode:
			var name string
			if err := elem.Decode(&name); err != nil {
				return fmt.Errorf("secrets[%d]: %w", i, err)
			}
			return fmt.Errorf("secrets[%d]: bare string %q is not allowed; use the typed form `- {name: %s, required: true}`",
				i, name, name)
		default:
			return fmt.Errorf("secrets[%d]: expected a mapping, got %s", i, nodeKindName(elem.Kind))
		}
	}
	*s = out
	return nil
}

// MarshalYAML emits the typed form.
func (s SecretsField) MarshalYAML() (any, error) {
	if len(s) == 0 {
		return nil, nil
	}
	out := make([]SecretEntry, len(s))
	copy(out, s)
	return out, nil
}

// UnmarshalYAML on Pipeline rejects the pre-v0.6 `targets:` and
// `args:` keys with a clear migration message pointing at the
// entrypoint-vs-pipeline split. Both shapes were removed wholesale
// in v0.6 in favor of one-pipeline-per-deployment-shape; the
// migration is the docs/migrations/v0.6.0.md walkthrough.
func (p *Pipeline) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return p.UnmarshalYAML(node.Alias)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("pipeline: expected a mapping, got %s", nodeKindName(node.Kind))
	}
	var name string
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "name" {
			_ = node.Content[i+1].Decode(&name)
			break
		}
	}
	known := pipelineKnownYAMLFields()
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		if _, ok := known[key.Value]; !ok {
			return fmt.Errorf("pipeline %q: unknown field %q", name, key.Value)
		}
	}
	type pipelineAlias Pipeline
	var raw pipelineAlias
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*p = Pipeline(raw)
	return nil
}

// pipelineKnownYAMLFields returns the set of YAML keys Pipeline
// declares. The custom UnmarshalYAML bypasses decoder-level
// KnownFields strictness (node.Decode doesn't inherit it), so we
// re-implement the check here against this canonical set.
func pipelineKnownYAMLFields() map[string]struct{} {
	return map[string]struct{}{
		"name": {}, "entrypoint": {}, "description": {},
		"on": {}, "secrets": {},
		"guards": {}, "args": {}, "profile": {},
	}
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
// a pipeline with no triggers is manually invocable via
// `sparkwing run <name>`.
type Triggers struct {
	Push     *PushTrigger     `yaml:"push,omitempty"`
	Schedule string           `yaml:"schedule,omitempty"`
	Webhook  *WebhookTrigger  `yaml:"webhook,omitempty"`
	PreHook  *PreHookTrigger  `yaml:"pre_commit,omitempty"`
	PostHook *PostHookTrigger `yaml:"pre_push,omitempty"`
}

// PushTrigger fires on git push events matching the rules.
type PushTrigger struct {
	Branches []string `yaml:"branches,omitempty"`
	Paths    []string `yaml:"paths,omitempty"`
}

// WebhookTrigger exposes an HTTP path that fires the pipeline. The
// controller assembles a RunContext from the incoming request.
type WebhookTrigger struct {
	Path string `yaml:"path"`
}

// PreHookTrigger fires from a pre-commit git hook. Scoped to fast
// local checks.
type PreHookTrigger struct{}

// PostHookTrigger fires from a pre-push git hook. Scoped to heavier
// checks like full test suites.
type PostHookTrigger struct{}

// Parse decodes a pipelines config from r (the pipelines: section of
// sparkwing.yaml, as a standalone document). Retained for tests and
// round-trip helpers; project config is read via pkg/projectconfig.
func Parse(r io.Reader) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
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
// config.
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
		if err := p.Guards.Validate(p.Name); err != nil {
			return err
		}
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
// start. Defaults to true when neither field is set.
func (e SecretEntry) IsRequired() bool {
	return !e.Optional
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

// EachPipeline calls fn for every (pipeline name, entrypoint name)
// pair in file order. Matches the iteration shape sparkwing's
// BindPipelinesFromYAML expects, avoiding a hard import cycle from
// pkg/pipelines into sparkwing.
func (c *Config) EachPipeline(fn func(name, entrypoint string)) {
	if c == nil {
		return
	}
	for _, p := range c.Pipelines {
		fn(p.Name, p.Entrypoint)
	}
}

// PipelinesByEntrypoint returns every pipeline keyed by its entrypoint
// type name. Multiple pipelines can share an entrypoint (the whole
// point of the v0.6 redesign); the returned slice preserves file
// order within each entrypoint bucket.
func (c *Config) PipelinesByEntrypoint() map[string][]*Pipeline {
	out := map[string][]*Pipeline{}
	for i := range c.Pipelines {
		p := &c.Pipelines[i]
		out[p.Entrypoint] = append(out[p.Entrypoint], p)
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
	}
	return true
}
