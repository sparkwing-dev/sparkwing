package pipelines

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the whole pipelines.yaml contents.
type Config struct {
	Pipelines []Pipeline `yaml:"pipelines"`
}

// Pipeline is one registry entry. A pipeline binds a Go entrypoint
// (declared via [sparkwing.RegisterEntrypoint]) to one named
// deployment shape: defaults, guards, locked flags, dispatch metadata,
// triggers, secrets surface. One Go entrypoint can back many
// pipelines, each with its own policy.
type Pipeline struct {
	Name        string       `yaml:"name"`
	Entrypoint  string       `yaml:"entrypoint"`
	Description string       `yaml:"description,omitempty"`
	On          Triggers     `yaml:"on,omitempty"`
	Secrets     SecretsField `yaml:"secrets,omitempty"`
	Tags        []string     `yaml:"tags,omitempty"`

	// Hidden omits the entry from default `sparkwing run <TAB>`
	// listings. Still invocable by typing the exact name.
	Hidden bool `yaml:"hidden,omitempty"`

	// Guards gate dispatch on the resolved profile + args. Reject
	// fires before any step runs when any token matches; Require
	// fires when not every token matches. Token vocabulary:
	// `profile-local`, `profile-controller`, `profile-name:<name>`,
	// `arg:<flag>=<value>`. See pkg/pipelines/guards.go.
	Guards Guards `yaml:"guards,omitempty"`

	// Defaults supplies per-arg fallback values. Lower priority than
	// schema Computed and explicit CLI flags; higher priority than
	// schema Default. Keyed by CLI flag name (kebab-case, matching
	// what the SDK's WithArgs[T] field tags resolve to).
	Defaults map[string]string `yaml:"defaults,omitempty"`

	// Locked lists CLI flag names the operator may not override. The
	// CLI rejects `--<name>` invocations with a clear error naming
	// the locking pipeline. Use when a pipeline must enforce policy
	// the operator can't unset (e.g. `locked: [protected]` on the
	// prod-deployment pipeline).
	Locked []string `yaml:"locked,omitempty"`

	// Dispatch carries the per-pipeline scheduling metadata: runner
	// allowlist, secret source, protected gate, approvals, optional
	// backend overrides. Absent block means "laptop default":
	// in-process runner, no source binding, no protection gate.
	Dispatch *Dispatch `yaml:"dispatch,omitempty"`

	// Values is the layered config-value surface for this pipeline.
	// See PipelineValues for the layering rule.
	Values PipelineValues `yaml:"values,omitempty"`
}

// Dispatch carries one pipeline's scheduling metadata. All fields
// are optional; an absent block (Pipeline.Dispatch == nil) means the
// pipeline runs in the laptop default shape (in-process runner, no
// secret source, no protection gate).
type Dispatch struct {
	// Runners is the runner pool / label allowlist. Empty means
	// any runner is acceptable. Layered atop the per-job Requires
	// declared via the SDK's RequiresProvider interface.
	Runners []string `yaml:"runners,omitempty"`

	// Source names an entry in sources.yaml that resolves Secret /
	// Config calls. Empty means fall back to the global default
	// declared in sources.yaml.
	Source string `yaml:"source,omitempty"`

	// Approvals, when non-empty, gates dispatch on a human response
	// before any jobs run. Today only "required" is accepted.
	Approvals string `yaml:"approvals,omitempty"`

	// Protected refuses non-default-branch sources and surfaces a
	// loud banner in the dashboard. Use on production-binding
	// pipelines to keep an ad-hoc branch run from reaching real
	// infra.
	Protected bool `yaml:"protected,omitempty"`

	// Backend overrides cache / logs / state destinations for runs
	// against this pipeline, layered on top of the resolved
	// profile's surfaces. Per-surface shape is intentionally left
	// untyped here so it accepts any backend spec without a parser
	// update.
	Backend *DispatchBackend `yaml:"backend,omitempty"`
}

// DispatchBackend carries per-surface backend overrides for runs
// against a pipeline. Each surface stays untyped (map[string]any) so
// callers can declare any backend spec without a parser update here.
type DispatchBackend struct {
	Cache map[string]any `yaml:"cache,omitempty"`
	Logs  map[string]any `yaml:"logs,omitempty"`
	State map[string]any `yaml:"state,omitempty"`
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

// PipelineValues is the layered config-value surface declared on a
// pipeline. Base applies to every run; Runners is a per-runner
// overlay keyed by the runner name (matching the runners: block in
// sparkwing.yaml).
type PipelineValues struct {
	// Base values applied to every run regardless of runner.
	Base map[string]any `yaml:"base,omitempty"`

	// Runners is a per-runner overlay, applied after Base. Key is the
	// runner name from the runners: block.
	Runners map[string]map[string]any `yaml:"runners,omitempty"`
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
		switch key.Value {
		case "targets":
			return fmt.Errorf(
				"pipeline %q: `targets:` was removed in v0.6; one pipeline now binds one deployment shape. "+
					"Split into N pipelines (e.g. deploy-dev, deploy-prod), each with its own dispatch block. "+
					"See docs/migrations/v0.6.0.md",
				name,
			)
		case "args":
			return fmt.Errorf(
				"pipeline %q: `args:` was reshaped in v0.6 -- use top-level `defaults:` for per-arg defaults "+
					"and `dispatch:` for runner / source / protected / backend. See docs/migrations/v0.6.0.md",
				name,
			)
		case "runners":
			return fmt.Errorf(
				"pipeline %q: top-level `runners:` was moved under `dispatch:` in v0.6. Wrap with: dispatch: { runners: [...] }",
				name,
			)
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
		"on": {}, "secrets": {}, "tags": {}, "hidden": {},
		"guards": {}, "defaults": {}, "locked": {},
		"dispatch": {}, "values": {},
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
// a pipeline with no triggers can still be invoked manually via
// `sparkwing run <name>`.
type Triggers struct {
	Manual   *ManualTrigger   `yaml:"manual,omitempty"`
	Push     *PushTrigger     `yaml:"push,omitempty"`
	Schedule string           `yaml:"schedule,omitempty"`
	Webhook  *WebhookTrigger  `yaml:"webhook,omitempty"`
	Deploy   *DeployTrigger   `yaml:"deploy,omitempty"`
	PreHook  *PreHookTrigger  `yaml:"pre_commit,omitempty"`
	PostHook *PostHookTrigger `yaml:"pre_push,omitempty"`
}

// ManualTrigger is the explicit opt-in for `sparkwing run <name>`.
// Pipelines without any trigger declared are still manually
// invocable; this exists so authors can tag a pipeline as manual-only
// for clarity.
type ManualTrigger struct{}

// PushTrigger fires on git push events matching the rules.
type PushTrigger struct {
	Branches []string `yaml:"branches,omitempty"`
	Paths    []string `yaml:"paths,omitempty"`
	// Values overlays onto the pipeline's typed Config struct for
	// runs initiated by this trigger. Layered after Pipeline.Values.Base
	// by sparkwing.ResolvePipelineConfig.
	Values map[string]any `yaml:"values,omitempty"`
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
		if err := p.Dispatch.Validate(p.Name); err != nil {
			return err
		}
		if err := p.Guards.Validate(p.Name); err != nil {
			return err
		}
		if err := p.validateLocked(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks Dispatch's structural invariants. Today only the
// Approvals value is constrained; future approvals types
// ("two-person", etc.) extend the accepted set.
func (d *Dispatch) Validate(pipeline string) error {
	if d == nil {
		return nil
	}
	switch d.Approvals {
	case "", "required":
		// ok
	default:
		return fmt.Errorf("pipeline %q dispatch: approvals = %q is not a recognized value (accepted: required)",
			pipeline, d.Approvals)
	}
	return nil
}

// validateLocked ensures locked flag names are well-formed (non-empty,
// no duplicates). The actual lock enforcement happens at CLI parse
// time when an operator passes a locked --flag.
func (p *Pipeline) validateLocked() error {
	seen := map[string]struct{}{}
	for _, name := range p.Locked {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("pipeline %q: locked entry is empty", p.Name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("pipeline %q: locked entry %q is duplicated", p.Name, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// LockedSet returns the locked flag names as a set for O(1) lookups
// from the CLI flag parser.
func (p *Pipeline) LockedSet() map[string]struct{} {
	if len(p.Locked) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(p.Locked))
	for _, n := range p.Locked {
		out[strings.TrimSpace(n)] = struct{}{}
	}
	return out
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

// TriggerValues returns the values: block declared on the trigger
// spec whose source matches the run's TriggerInfo.Source string
// ("push"). Returns nil when no matching spec is declared or the
// spec carries no Values.
func (p *Pipeline) TriggerValues(source string) map[string]any {
	if p == nil {
		return nil
	}
	switch source {
	case "push":
		if p.On.Push != nil {
			return p.On.Push.Values
		}
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
		if !strings.EqualFold(joinSorted(lp.Tags), joinSorted(p.Tags)) {
			return false
		}
	}
	return true
}

func joinSorted(s []string) string {
	cp := append([]string(nil), s...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}
