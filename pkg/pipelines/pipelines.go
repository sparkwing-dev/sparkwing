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
	// Name is the invocable name (`sparkwing run <name>`); must equal
	// the string passed to the SDK's Register call.
	Name string `yaml:"name"`

	// Entrypoint is the Go pipeline struct type that implements this
	// entry (equals the struct name). Required.
	Entrypoint string `yaml:"entrypoint"`

	// Description is the one-line summary surfaced by `pipeline list`.
	Description string `yaml:"description,omitempty"`

	// On declares the triggers that auto-fire this pipeline. Absent
	// means manual-only (a command invoked by name).
	On Triggers `yaml:"on,omitempty"`

	// Hidden omits the entry from default `pipeline list` output; it
	// stays invocable by exact name and shows under `list --all`.
	Hidden bool `yaml:"hidden,omitempty"`

	// Guards gate dispatch on the resolved profile + args. Reject
	// fires before any step runs when any token matches; Require
	// fires when not every token matches. Token vocabulary:
	// `profile:local`, `profile:controller`, `profile:name=<name>`,
	// `arg:<flag>=<value>`. See pkg/pipelines/guards.go.
	Guards Guards `yaml:"guards,omitempty"`

	// Args supplies per-arg default values. Higher priority than
	// schema Default and Computed; lower than an explicit operator
	// CLI flag. Keyed by CLI flag name (kebab-case, matching what
	// the SDK's WithArgs[T] field tags resolve to).
	Args map[string]string `yaml:"args,omitempty"`

	// Profile names the project profile (from sparkwing.yaml's
	// profiles map) this pipeline uses. Empty means "fall back to
	// the project's defaults.profile selector". The CLI's --profile
	// flag (which targets ~/.config/sparkwing/profiles.yaml)
	// overrides this when present.
	Profile string `yaml:"profile,omitempty"`

	// Requires are runner-label requirements all jobs in this
	// pipeline must satisfy in addition to their own Job.Requires().
	// Wholesale replaces defaults.requires when non-empty. The
	// reserved label "local" pins execution to the in-process
	// runner (same effect as --sw-local-only).
	Requires []string `yaml:"requires,omitempty"`
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

// SecretEntry is one secret declaration. Required/Optional are
// mutually exclusive; when neither is set the entry is treated as
// required (see IsRequired).
type SecretEntry struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// SecretsField is the orchestrator's snapshot/wire format for a
// run's declared secret needs. Populated from a pipeline's
// Secrets() provider via reflection; shipped to cluster pods in the
// plan snapshot so they can re-resolve against their own backend.
type SecretsField []SecretEntry

// UnmarshalYAML decodes a Pipeline mapping and rejects any field not
// in pipelineKnownYAMLFields(). The strict check protects against
// typos and silently-dropped renamed keys; node.Decode skips
// decoder-level KnownFields strictness so we re-implement it here.
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
		"on": {}, "hidden": {},
		"guards": {}, "args": {}, "profile": {}, "requires": {},
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
	// Push fires on a git push the controller receives via webhook.
	Push *PushTrigger `yaml:"push,omitempty"`
	// Schedule is a cron expression the controller evaluates.
	Schedule string `yaml:"schedule,omitempty"`
	// Webhook exposes a custom HTTP path that fires the pipeline.
	Webhook *WebhookTrigger `yaml:"webhook,omitempty"`
	// PreHook fires from the installed git pre-commit hook.
	PreHook *PreHookTrigger `yaml:"pre_commit,omitempty"`
	// PostHook fires from the installed git pre-push hook.
	PostHook *PostHookTrigger `yaml:"pre_push,omitempty"`
}

// PushTrigger fires on git push events matching the rules.
type PushTrigger struct {
	// Branches limits the trigger to pushes on these branches (glob
	// patterns); empty matches any branch.
	Branches []string `yaml:"branches,omitempty"`
	// Paths limits the trigger to pushes touching these path globs;
	// empty matches any path.
	Paths []string `yaml:"paths,omitempty"`
}

// WebhookTrigger exposes an HTTP path that fires the pipeline. The
// controller assembles a RunContext from the incoming request.
type WebhookTrigger struct {
	// Path is the HTTP path the controller exposes to fire the
	// pipeline (e.g. /review).
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

		if err := p.Guards.Validate(p.Name); err != nil {
			return err
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
