package pipelines

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
)

// Config is one pipeline registry document.
type Config struct {
	Pipelines []Pipeline `yaml:"pipelines"`
}

// Pipeline is one registry entry. A pipeline binds a Go entrypoint
// (declared via [sparkwing.RegisterEntrypoint]) to one named
// deployment shape: defaults, guards, dispatch metadata, and triggers.
// One Go entrypoint can back many pipelines, each with its own policy.
type Pipeline struct {
	// Name is the invocable name (`sparkwing run <name>`); must equal
	// the string passed to the SDK's Register call. It must match
	// `^[A-Za-z0-9][A-Za-z0-9._-]*$`.
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

	// Guards gate dispatch on the resolved profile, args, and git
	// branch. Reject fires before any step runs when any token
	// matches; Require fires when not every token matches. Token
	// vocabulary: `profile:local`, `profile:controller`,
	// `profile:name=<name>`, `arg:<flag>=<value>`,
	// `git:branch=<name>`, `git:branch=default`. A literal branch matches
	// the checked-out head. The default token matches only when the
	// dispatch supplies default-branch metadata. `arg:` tokens read the
	// merged argument set the run executes with, so a value supplied by
	// defaults.args or this entry's own args: block is guarded exactly
	// like one typed on the command line. See pkg/pipelines/guards.go.
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
	// reserved label "local" keeps fleet helpers from claiming the
	// node; --sw-local-only instead selects local storage backends.
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
	// PullRequest fires on a GitHub pull_request event the controller
	// receives via webhook. The run checks out the PR head; base ref
	// and PR number reach the pipeline on RunContext.Trigger.PullRequest.
	PullRequest *PullRequestTrigger `yaml:"pull_request,omitempty"`
	// Schedule fires the pipeline on one or more cron cadences. It accepts
	// a bare cron string, one mapping, or a list of mappings; every entry
	// declares where it fires.
	Schedule ScheduleTriggers `yaml:"schedule,omitempty"`
	// Webhook exposes a custom HTTP path that fires the pipeline.
	Webhook *WebhookTrigger `yaml:"webhook,omitempty"`
	// PreHook fires from the installed git pre-commit hook.
	PreHook *PreHookTrigger `yaml:"pre_commit,omitempty"`
	// PostHook fires from the installed git pre-push hook.
	PostHook *PostHookTrigger `yaml:"pre_push,omitempty"`
	// PostCommitHook fires from the installed git post-commit hook,
	// after the commit is recorded. It never blocks or aborts the
	// commit.
	PostCommitHook *PostCommitHookTrigger `yaml:"post_commit,omitempty"`
}

// PushTrigger records intent for GitHub push events. The controller dispatches
// the pipeline named by the webhook URL without evaluating Branches or Paths.
type PushTrigger struct {
	// Branches records the intended push branch globs. It does not gate
	// webhook dispatch.
	Branches []string `yaml:"branches,omitempty"`
	// Paths records the intended changed-path globs. It does not gate
	// webhook dispatch.
	Paths []string `yaml:"paths,omitempty"`
}

// PullRequestTrigger fires on GitHub pull_request events. The
// controller dispatches on the opened, synchronize, and reopened
// actions; other actions (labeled, closed, ...) are acknowledged and
// ignored. The run checks out the PR head commit, and the pipeline
// reads the base ref, head ref, and PR number from
// RunContext.Trigger.PullRequest.
//
// Actions and Branches are declarative filters that record intent.
// Like on.push's branches/paths, the controller does not gate on them
// today (it applies the default action set and dispatches whichever
// pipeline the webhook URL names). A pipeline branch guard can gate the
// checked-out branch, but it does not match the pull request's base branch.
type PullRequestTrigger struct {
	// Actions records the intended pull_request actions. The controller
	// applies its opened, synchronize, and reopened set independently.
	Actions []string `yaml:"actions,omitempty"`
	// Branches records the intended pull-request base branch globs. It does
	// not gate webhook dispatch.
	Branches []string `yaml:"branches,omitempty"`
}

// ScheduleTriggers is one pipeline's declared cadences, one entry per cadence, nil when the
// pipeline declares none. Sparkwing evaluates a `local` entry on every host where
// `sparkwing crons install` armed the pipeline.
//
// The YAML accepts a bare cron string, one mapping, or a list of mappings:
//
//	schedule:
//	  cron: "0 3 * * *"
//	  where: local
//
//	schedule:
//	  - name: nightly
//	    cron: "0 3 * * *"
//	    where: local
//	  - name: cluster
//	    cron: "0 3 * * *"
//	    tz: America/Denver
//	    where: controller
//	    args:
//	      region: us-east
type ScheduleTriggers []ScheduleTrigger

// ScheduleTrigger is one cadence: when it fires, which side fires it, and the policy that
// resolves a fire the host was not awake for.
type ScheduleTrigger struct {
	// Name distinguishes several cadences on one pipeline and appears in `sparkwing crons list`
	// as <repo>/<pipeline>/<name>. Required once a pipeline declares more than one entry; a lone
	// entry is named "default". It matches `^[a-z0-9][a-z0-9-]*$` and is at most 40 characters.
	Name string `yaml:"name,omitempty"`
	// Cron is a five-field cron expression (minute hour day-of-month month day-of-week) with the usual
	// lists, ranges, steps, month and day names, and the @hourly/@daily/@weekly/@monthly/@yearly aliases.
	Cron string `yaml:"cron"`
	// Where says which side fires this entry: "local" fires from a host that armed it with
	// `sparkwing crons install`, "controller" from a controller it was installed on. One side per
	// entry, and there is no default, so nothing fires somewhere you did not say it should.
	// Declare one entry per side to fire from both.
	Where string `yaml:"where"`
	// TZ is the IANA zone the expression is read in, such as America/Denver. Default UTC. The word
	// "local" means the zone of the host that runs the schedule.
	TZ string `yaml:"tz,omitempty"`
	// Overlap decides what happens when the cadence comes due while the previous scheduled run is still
	// running: "skip" (default) records the fire as skipped, "queue" launches it anyway and lets admission order it.
	Overlap string `yaml:"overlap,omitempty"`
	// CatchUp is how long after its due minute a fire may still happen when the host was asleep or the
	// timer was late, as a Go duration such as 1h or 30m. Default 1h; values under 2m are rejected. A due
	// minute older than the window is recorded as missed.
	CatchUp string `yaml:"catch_up,omitempty"`
	// Args supplies argument values for this cadence's runs, keyed by CLI flag name exactly like
	// `args:` on the pipeline. They sit above pipeline.args and below a host's own override for the
	// schedule, so guards' `arg:` tokens read them and the fire records what it ran with.
	Args map[string]string `yaml:"args,omitempty"`
}

// Schedule field defaults and the vocabulary the validator accepts.
const (
	// DefaultScheduleName is the name a lone unnamed entry carries.
	DefaultScheduleName = "default"
	// DefaultScheduleTZ is the zone an empty tz reads the expression in.
	DefaultScheduleTZ = "UTC"
	// ScheduleTZLocal is the tz value meaning the zone of the host that runs the schedule.
	ScheduleTZLocal = "local"
	// ScheduleWhereLocal fires the entry from a host that armed it with `sparkwing crons install`.
	ScheduleWhereLocal = "local"
	// ScheduleWhereController fires the entry from a controller the schedule was installed on.
	ScheduleWhereController = "controller"
	// ScheduleOverlapSkip records a fire as skipped while the previous scheduled run is still running.
	ScheduleOverlapSkip = "skip"
	// ScheduleOverlapQueue launches a due fire even while the previous scheduled run is still running.
	ScheduleOverlapQueue = "queue"
	// DefaultScheduleOverlap is the policy an empty overlap means.
	DefaultScheduleOverlap = ScheduleOverlapSkip
)

// DefaultScheduleCatchUp is the window an empty catch_up means.
const DefaultScheduleCatchUp = time.Hour

// MaxScheduleNameLen is the longest schedule name the validator accepts.
const MaxScheduleNameLen = 40

// safety: the name reaches display names, argv and file paths the same way a pipeline name does.
var scheduleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var argFlagNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func scheduleKnownYAMLFields() map[string]struct{} {
	return map[string]struct{}{
		"name": {}, "cron": {}, "where": {}, "tz": {}, "overlap": {}, "catch_up": {}, "args": {},
	}
}

// UnmarshalYAML decodes the scalar form, which sets one entry's Cron alone, a single mapping, or a
// list of mappings.
func (s *ScheduleTriggers) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return s.UnmarshalYAML(node.Alias)
	}
	switch {
	case node.Tag == "!!null":
		*s = nil
		return nil
	case node.Kind == yaml.ScalarNode:
		var cron string
		if err := node.Decode(&cron); err != nil {
			return fmt.Errorf("on.schedule: %w", err)
		}
		*s = ScheduleTriggers{{Cron: cron}}
		return nil
	case node.Kind == yaml.MappingNode:
		var one ScheduleTrigger
		if err := node.Decode(&one); err != nil {
			return err
		}
		*s = ScheduleTriggers{one}
		return nil
	case node.Kind == yaml.SequenceNode:
		out := make(ScheduleTriggers, 0, len(node.Content))
		for _, item := range node.Content {
			var one ScheduleTrigger
			if err := item.Decode(&one); err != nil {
				return err
			}
			out = append(out, one)
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("on.schedule: expected a cron string, a mapping, or a list of mappings, got %s",
			nodeKindName(node.Kind))
	}
}

// UnmarshalYAML decodes one schedule entry and rejects any key outside the schema.
func (s *ScheduleTrigger) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return s.UnmarshalYAML(node.Alias)
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("on.schedule: expected a mapping, got %s", nodeKindName(node.Kind))
	}
	known := scheduleKnownYAMLFields()
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		if _, ok := known[key.Value]; !ok {
			return fmt.Errorf("on.schedule: unknown field %q", key.Value)
		}
	}
	type scheduleAlias ScheduleTrigger
	var raw scheduleAlias
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("on.schedule: %w", err)
	}
	*s = ScheduleTrigger(raw)
	return nil
}

// EffectiveName returns the entry's name, or "default" when it declares none.
func (s *ScheduleTrigger) EffectiveName() string {
	if s.Name == "" {
		return DefaultScheduleName
	}
	return s.Name
}

// Location resolves TZ to the zone the cron expression is read in.
func (s *ScheduleTrigger) Location() (*time.Location, error) {
	switch s.TZ {
	case "", DefaultScheduleTZ:
		return time.UTC, nil
	case ScheduleTZLocal:
		return time.Local, nil
	}
	loc, err := time.LoadLocation(s.TZ)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", s.TZ)
	}
	return loc, nil
}

// OverlapPolicy returns the declared overlap policy, or the default when unset.
func (s *ScheduleTrigger) OverlapPolicy() string {
	if s.Overlap == "" {
		return DefaultScheduleOverlap
	}
	return s.Overlap
}

// CatchUpDuration returns the declared catch-up window, or the default when unset.
func (s *ScheduleTrigger) CatchUpDuration() (time.Duration, error) {
	if s.CatchUp == "" {
		return DefaultScheduleCatchUp, nil
	}
	d, err := time.ParseDuration(s.CatchUp)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s.CatchUp)
	}
	return d, nil
}

// Validate reports any problem in the declared cadences, naming the pipeline and the entry at
// fault. Every entry needs a where; a pipeline declaring more than one needs a name on each, and
// the names must be unique within the pipeline. An empty list is valid: the pipeline declares no
// cadence.
func (s ScheduleTriggers) Validate(pipeline string) error {
	named := len(s) > 1
	seen := make(map[string]struct{}, len(s))
	for i := range s {
		entry := &s[i]
		if named && entry.Name == "" {
			return fmt.Errorf("pipeline %q: on.schedule[%d].name is required once a pipeline declares more than one schedule", pipeline, i)
		}
		if err := entry.Validate(pipeline); err != nil {
			return err
		}
		name := entry.EffectiveName()
		if _, dup := seen[name]; dup {
			return fmt.Errorf("pipeline %q schedule %q: duplicate schedule name", pipeline, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// Validate reports any problem in the entry, naming the pipeline, the entry, and the field at
// fault. It does not check the entry's name against its siblings; ScheduleTriggers.Validate does.
func (s *ScheduleTrigger) Validate(pipeline string) error {
	if s == nil {
		return nil
	}
	label := fmt.Sprintf("pipeline %q schedule %q", pipeline, s.EffectiveName())
	if s.Name != "" {
		if !scheduleNamePattern.MatchString(s.Name) {
			return fmt.Errorf("%s: on.schedule.name must match %s", label, scheduleNamePattern)
		}
		if len(s.Name) > MaxScheduleNameLen {
			return fmt.Errorf("%s: on.schedule.name must be at most %d characters", label, MaxScheduleNameLen)
		}
	}
	if strings.TrimSpace(s.Cron) == "" {
		return fmt.Errorf("%s: on.schedule.cron is required", label)
	}
	if _, err := cronspec.Parse(s.Cron); err != nil {
		return fmt.Errorf("%s: on.schedule.cron: %w", label, err)
	}
	switch s.Where {
	case ScheduleWhereLocal, ScheduleWhereController:
	case "":
		return fmt.Errorf("%s: on.schedule.where is required: a schedule must say where it fires, %q or %q; declare an entry per side to fire from both",
			label, ScheduleWhereLocal, ScheduleWhereController)
	default:
		return fmt.Errorf("%s: on.schedule.where: must be %q or %q, got %q",
			label, ScheduleWhereLocal, ScheduleWhereController, s.Where)
	}
	if _, err := s.Location(); err != nil {
		return fmt.Errorf("%s: on.schedule.tz: %w", label, err)
	}
	switch s.OverlapPolicy() {
	case ScheduleOverlapSkip, ScheduleOverlapQueue:
	default:
		return fmt.Errorf("%s: on.schedule.overlap: must be %q or %q, got %q",
			label, ScheduleOverlapSkip, ScheduleOverlapQueue, s.Overlap)
	}
	catchUp, err := s.CatchUpDuration()
	if err != nil {
		return fmt.Errorf("%s: on.schedule.catch_up: %w", label, err)
	}
	if catchUp < cronspec.MinCatchUp {
		return fmt.Errorf("%s: on.schedule.catch_up: must be at least %s, got %s",
			label, cronspec.MinCatchUp, catchUp)
	}
	for _, key := range slices.Sorted(maps.Keys(s.Args)) {
		if !argFlagNamePattern.MatchString(key) {
			return fmt.Errorf("%s: on.schedule.args: %q is not a CLI flag name; keys must match %s",
				label, key, argFlagNamePattern)
		}
	}
	return nil
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

// PostCommitHookTrigger fires from a post-commit git hook. The commit
// has already landed, so the pipeline runs but never aborts it: the
// installed hook tolerates failures and always exits zero. Scoped to
// fast, non-blocking follow-ups (self-install, notifications).
type PostCommitHookTrigger struct{}

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
		return nil, fmt.Errorf("parse sparkwing.yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// safety: the name reaches shell-rendered git hooks, argv, log lines, and file paths.
var pipelineNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Validate returns an error describing any structural problem in the
// config.
func (c *Config) Validate() error {
	seen := map[string]struct{}{}
	for i, p := range c.Pipelines {
		if p.Name == "" {
			return fmt.Errorf("pipelines[%d]: name is required", i)
		}
		if !pipelineNamePattern.MatchString(p.Name) {
			return fmt.Errorf("pipeline %q: name must match %s", p.Name, pipelineNamePattern)
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
		if err := p.On.Schedule.Validate(p.Name); err != nil {
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
