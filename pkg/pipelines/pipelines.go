package pipelines

import (
	"errors"
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
	// Secrets is the declared secrets surface. Each entry is a
	// typed SecretEntry with explicit required/optional. See
	// SecretsField.
	Secrets SecretsField `yaml:"secrets,omitempty"`
	Tags    []string     `yaml:"tags,omitempty"`
	// Hidden omits the entry from default `sparkwing run <TAB>` listings.
	// It is still invocable by typing the exact name. Used for
	// rarely-used tools (demos, scaffolding, one-shot utilities)
	// that would otherwise clutter the completion menu.
	Hidden bool `yaml:"hidden,omitempty"`

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

// SecretsField is the typed list of secret declarations on a
// pipeline. Each entry is a mapping with name + required/optional:
//
//	secrets:
//	  - {name: DEPLOY_TOKEN, required: true}
//	  - {name: SLACK_HOOK,   optional: true}
//
// The custom UnmarshalYAML rejects the legacy bare-string form
// (`secrets: [FOO, BAR]`) with a clear migration message.
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

// ManualTrigger is the explicit opt-in for `sparkwing run <name>`. Pipelines
// without any trigger declared are still manually invocable; this
// exists so authors can tag a pipeline as manual-only for clarity.
type ManualTrigger struct{}

// PushTrigger fires on git push events matching the rules.
type PushTrigger struct {
	Branches []string `yaml:"branches"`
	Paths    []string `yaml:"paths"`
	// Values overlays onto the pipeline's typed Config struct for
	// runs initiated by this trigger. Layered after the per-target
	// values by sparkwing.ResolvePipelineConfig the same way
	// values.base and targets.<name>.values are. Use this to flip
	// typed Config fields per-trigger (e.g. push to main =>
	// deploy_env: staging) and read them via
	// sparkwing.PipelineConfig[T](ctx).
	Values map[string]any `yaml:"values,omitempty"`
	// Target defaults the run's --for selection when the trigger
	// fires without an explicit override. Closes the "push to main
	// with no --for skips every OnTarget job" gap: declare
	// push: { branches: [main], target: prod } and the trigger
	// dispatches release --for prod. A CLI --for still wins when
	// both are set.
	Target string `yaml:"target,omitempty"`
}

// WebhookTrigger exposes an HTTP path that fires the pipeline. The
// controller assembles a RunContext from the incoming request.
type WebhookTrigger struct {
	Path string `yaml:"path"`
	// Target defaults the run's --for selection for webhook-fired
	// runs. Same precedence as PushTrigger.Target: CLI / payload
	// override wins, this value is the fallback.
	Target string `yaml:"target,omitempty"`
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
		if err := p.validateTriggerTargets(); err != nil {
			return err
		}
	}
	return nil
}

// validateTriggerTargets rejects trigger spec defaults that name an
// undeclared target. Surfaces the misconfiguration at parse rather
// than at the first push event.
func (p *Pipeline) validateTriggerTargets() error {
	checks := []struct {
		trigger string
		target  string
	}{}
	if p.On.Push != nil {
		checks = append(checks, struct {
			trigger string
			target  string
		}{"push", p.On.Push.Target})
	}
	if p.On.Webhook != nil {
		checks = append(checks, struct {
			trigger string
			target  string
		}{"webhook", p.On.Webhook.Target})
	}
	for _, c := range checks {
		if c.target == "" {
			continue
		}
		if len(p.Targets) == 0 {
			return fmt.Errorf("pipeline %q: %s trigger declares target %q but pipeline declares no targets; declare a targets block or remove the trigger target",
				p.Name, c.trigger, c.target)
		}
		if !p.HasTarget(c.target) {
			return fmt.Errorf("pipeline %q: %s trigger target %q is not a declared target; declared: %v",
				p.Name, c.trigger, c.target, p.TargetNames())
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
	return !e.Optional
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

// TriggerTarget returns the default --for selection declared on the
// trigger spec whose source matches the run's TriggerInfo.Source
// string ("push", "webhook"). Returns "" when no matching spec is
// declared or the spec carries no Target.
//
// Used by the orchestrator at run start to apply a per-trigger
// default when the CLI / payload didn't pass an explicit --for. CLI
// --for overrides this value.
func (p *Pipeline) TriggerTarget(source string) string {
	if p == nil {
		return ""
	}
	switch source {
	case "push":
		if p.On.Push != nil {
			return p.On.Push.Target
		}
	case "webhook":
		if p.On.Webhook != nil {
			return p.On.Webhook.Target
		}
	}
	return ""
}

// TriggerValues returns the values: block declared on the trigger
// spec whose source matches the run's TriggerInfo.Source string
// ("push", "webhook", "schedule", ...). Returns nil when no
// matching spec is declared or the spec carries no Values.
//
// Used by sparkwing.ResolvePipelineConfig to layer per-trigger
// typed values onto the Config struct.
//
// Trigger sources that don't carry a values block today (manual,
// schedule, webhook, deploy, pre/post-hook) return nil. Adding
// Values to those is a future addition once a concrete use case
// lands.
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
