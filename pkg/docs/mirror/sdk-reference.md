<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference

Every exported symbol in the `sparkwing` package (the SDK you import as `sw`), generated from source. Browse the same thing with cross-links on pkg.go.dev: <https://pkg.go.dev/github.com/sparkwing-dev/sparkwing/sparkwing>. For concepts and usage examples, see [sdk.md](sdk.md).

## Functions

- `func Annotate(ctx context.Context, msg string)` -- Annotate records a persistent, human-readable summary string on the currently-executing Job.
- `func Arg[T any](ctx context.Context, name string) (T, error)` -- Arg returns a single resolved arg by its CLI flag name.
- `func ArgOrDefault[T any](ctx context.Context, name string, d T) T` -- ArgOrDefault is the convenience wrapper that returns d when the arg isn't present or doesn't unmarshal to T. Useful for steps that want to read an optional arg without surfacing an error.
- `func BindPipelinesFromYAML(cfg interface { EachPipeline(func(name, entrypoint string)) })` -- BindPipelinesFromYAML walks every pipeline entry in cfg and installs a Registration under the pipeline's name, sharing the Invoke / Schema / Instance of the registered entrypoint.
- `func Config(ctx context.Context, name string) (string, error)` -- Config resolves a non-secret config value through the same store as Secret.
- `func Debug(ctx context.Context, format string, args ...any)` -- Debug emits a debug-level LogRecord.
- `func DebugEnabled() bool` -- DebugEnabled reports whether SDK-internal verbose logging is on.
- `func Error(ctx context.Context, format string, args ...any)` -- Error emits an error-level message.
- `func GithubOwnerRepo(slug string) (owner, repo string)` -- GithubOwnerRepo splits a "owner/name" slug into its parts.
- `func Glob(pattern string) ([]string, error)` -- Glob expands a shell-style glob pattern.
- `func Info(ctx context.Context, format string, args ...any)` -- Info emits an info-level message to the active logger.
- `func Inputs[T any](ctx context.Context) T` -- Inputs returns the typed Inputs struct that the orchestrator parsed for the current run -- the same value the pipeline's Plan(ctx, plan, in T, rc) method received.
- `func IsDryRun(ctx context.Context) bool` -- IsDryRun reports whether ctx is in dry-run mode.
- `func MustConfig(ctx context.Context, name string) string` -- MustConfig is Config that panics on error.
- `func MustSecret(ctx context.Context, name string) string` -- MustSecret is Secret that panics on error.
- `func NodeFromContext(ctx context.Context) string` -- NodeFromContext returns the currently-executing node ID, or "" if unset.
- `func Path(parts ...string) string` -- Path joins parts onto WorkDir() and returns the absolute path.
- `func PipelineSecrets[T any](ctx context.Context) *T` -- PipelineSecrets returns the typed Secrets struct installed on ctx, or nil when the pipeline doesn't implement SecretsProvider.
- `func ReadFile(path string) ([]byte, error)` -- ReadFile reads the named file.
- `func Register[T any](name string, factory func() Pipeline[T])` -- Register installs a pipeline under the given name.
- `func RegisterEntrypoint[T any](entrypointName string, factory func() Pipeline[T])` -- RegisterEntrypoint installs a Go work unit (the entrypoint) under the given type-name, matching the `entrypoint:` field in sparkwing.yaml.
- `func Registered() []string` -- Registered returns the names of all registered pipelines, sorted.
- `func ResolveAs[T any](s *Schema, in ResolveInputs) (T, error)` -- ResolveAs is the typed convenience wrapper: same semantics as Schema.Resolve but returns T directly so callers don't have to type-assert the reflect.Value.
- `func RunAndAwait[Out, In any](ctx context.Context, pipeline, nodeID string, opts ...AwaitOption) (Out, error)` -- RunAndAwait triggers a fresh run of pipeline and waits for it to reach terminal state, returning the typed output of nodeID from that run.
- `func RunWork(ctx context.Context, w *Work) (any, error)` -- RunWork executes w's step + spawn DAG.
- `func Secret(ctx context.Context, name string) (string, error)` -- Secret resolves a masked value through the resolver installed on ctx.
- `func SetGit(g *Git)` -- SetGit attaches a fully-populated Git to the runtime.
- `func SetWorkDir(dir string)` -- SetWorkDir overrides the WorkDir field on the runtime singleton and updates the Git workDir so live methods follow.
- `func SkipArgResolve(ctx context.Context) context.Context` -- SkipArgResolve marks ctx so the registration's invoke() builds a plan without running the v0.6 args resolution+bind pass.
- `func StepFromContext(ctx context.Context) string` -- StepFromContext returns the active step ID, or "" outside a step.
- `func StepGet[T any](ctx context.Context, step *WorkStep) T` -- StepGet blocks until step has completed, then returns its typed output as T. Used inside another step's body when composing values from upstream typed steps.
- `func Summary(ctx context.Context, markdown string)` -- Summary records a persistent markdown run summary on the currently-executing Job or Step.
- `func TypeName(p any) string` -- TypeName returns the Go type name of p, suitable for matching against a sparkwing.yaml `entrypoint:` field.
- `func Warn(ctx context.Context, format string, args ...any)` -- Warn emits a warn-level message.
- `func WithCommandEnv(ctx context.Context, env map[string]string) context.Context` -- WithCommandEnv returns a context whose sparkwing.Exec/Bash calls inherit env.
- `func WithFailure(ctx context.Context, f Failure) context.Context` -- WithFailure returns a context carrying f, read back by a failure-aware recovery callback via FailureFromContext.
- `func WithResolvedArgs(ctx context.Context, args map[string]any) context.Context` -- WithResolvedArgs installs a resolved-args map on the context so sparkwing.Arg[T] / ArgOrDefault can read it from any step body.
- `func WithSecretResolver(ctx context.Context, r SecretResolver) context.Context` -- WithSecretResolver returns a derived ctx carrying the given resolver.
- `func WithStep(ctx context.Context, stepID string) context.Context` -- WithStep installs the active step ID into ctx so the breadcrumb on records emitted *inside* the step body carries it.
- `func WorkDir() string` -- WorkDir returns the pipeline working directory (the repo root).
- `func WriteFile(path string, data []byte) error` -- WriteFile writes data to the named file with perm 0o644 (creating or truncating).

## Types

### type AfterRunFn

AfterRunFn runs once after Run (including all retries) terminates.

```
type AfterRunFn func(ctx context.Context, err error)
```


### type ApprovalConfig

ApprovalConfig describes a manual approval gate.

```
type ApprovalConfig struct {
    // Message is the operator-facing prompt shown in the dashboard /
    // CLI. Empty falls back to a generic "Approve <node>?" in the UI.
    Message string
    // Timeout bounds how long the gate waits for a human answer. Zero
    // means never time out.
    Timeout time.Duration
    // OnExpiry controls how an unanswered gate resolves once Timeout
    // elapses. The zero value is ApprovalFail ("something went wrong,
    // the gate wasn't answered"). ApprovalDeny treats no-answer as a
    // soft "no" and ApprovalApprove as a soft "yes". Named OnExpiry
    // rather than OnTimeout to avoid confusion with Job.Timeout(),
    // which is unrelated (per-attempt execution budget).
    OnExpiry ApprovalTimeoutPolicy
}
```


### type ApprovalGate

ApprovalGate is the handle returned by sw.JobApproval.

```
type ApprovalGate struct {
    // contains filtered or unexported fields
}
```

- `func JobApproval(p *Plan, id string, cfg ApprovalConfig) *ApprovalGate` -- JobApproval registers a manual approval gate under id and returns the gate handle for further configuration.
- `func (g *ApprovalGate) AfterRun(fn AfterRunFn) *ApprovalGate` -- AfterRun registers a hook that runs after the gate resolves (regardless of outcome).
- `func (g *ApprovalGate) BeforeRun(fn BeforeRunFn) *ApprovalGate` -- BeforeRun registers a hook that runs before the gate is presented to the operator.
- `func (g *ApprovalGate) ContinueOnError() *ApprovalGate` -- ContinueOnError marks the gate so a failed resolution is treated as a soft failure for downstream propagation.
- `func (g *ApprovalGate) ID() string` -- ID returns the gate's node id.
- `func (g *ApprovalGate) Job() *JobNode` -- Job returns the underlying *JobNode.
- `func (g *ApprovalGate) Needs(deps ...Dep) *ApprovalGate` -- Needs declares hard upstream dependencies on the gate.
- `func (g *ApprovalGate) NeedsOptional(deps ...Dep) *ApprovalGate` -- NeedsOptional declares soft upstream dependencies; missing IDs are silently dropped instead of failing the run.
- `func (g *ApprovalGate) OnFailure(id string, x any) *ApprovalGate` -- OnFailure registers a recovery node that runs if the gate resolves to ApprovalFail.
- `func (g *ApprovalGate) Optional() *ApprovalGate` -- Optional marks the gate as optional: a non-Approve resolution doesn't fail downstream nodes whose only dep is this gate.
- `func (g *ApprovalGate) SkipIf(fn SkipPredicate, opts ...SkipOption) *ApprovalGate` -- SkipIf registers a predicate that skips the gate (treats it as resolved Approve) when fn returns true.

### type ApprovalTimeoutPolicy

ApprovalTimeoutPolicy enumerates the resolution applied to an unanswered approval gate when its Timeout elapses.

```
type ApprovalTimeoutPolicy string
```


### type AwaitOption

AwaitOption tunes RunAndAwait's trigger + wait behavior.

```
type AwaitOption func(*awaitConfig)
```

- `func WithFreshArgs(args map[string]string) AwaitOption` -- WithFreshArgs passes args through to the spawned trigger.
- `func WithFreshBranch(branch string) AwaitOption` -- WithFreshBranch overrides the branch the spawned trigger runs against.
- `func WithFreshInputs[T any](in T) AwaitOption` -- WithFreshInputs flattens a typed Inputs struct into the underlying args map.
- `func WithFreshRepo(repo string) AwaitOption` -- WithFreshRepo declares which repo the spawned pipeline lives in (e.g.
- `func WithFreshTimeout(d time.Duration) AwaitOption` -- WithFreshTimeout bounds the total wait.

### type AwaitRequest

AwaitRequest is the awaiter's input struct.

```
type AwaitRequest struct {
    Pipeline string
    NodeID   string
    Args     map[string]string
    Timeout  time.Duration
    // Repo, when non-empty, declares which repo the spawned pipeline
    // lives in. Required for cross-repo awaits; empty falls back to
    // parent-run inheritance.
    Repo string
    // Branch overrides the default branch for the spawned trigger
    // (effective only when Repo is also set). Empty -> "main".
    Branch string
}
```


### type Base

Base is the marker embedded by every pipeline.

```
type Base struct{}
```


### type BeforeRunFn

BeforeRunFn runs once before the first Run attempt.

```
type BeforeRunFn func(ctx context.Context) error
```


### type Cache

Cache is the artifact store interface the orchestrator and pipeline authors reach for.

```
type Cache = storage.ArtifactStore
```


### type CacheConfig

CacheConfig is a node's resolved content-cache configuration: the key function that names the work plus the retention window for a stored result.

```
type CacheConfig struct {
    // Key computes the content key after upstream dependencies
    // complete. Return [NoCache] to opt this invocation out.
    Key CacheKeyFn
    // TTL bounds how long a stored result remains reusable.
    TTL time.Duration
}
```


### type CacheKey

CacheKey is the content-addressed identifier for a node's work.

```
type CacheKey string
```

- `func Key(parts ...any) CacheKey` -- Key composes a CacheKey from arbitrary parts.
- `func (k CacheKey) IsNoCache() bool` -- IsNoCache reports whether k is the explicit NoCache sentinel.

### type CacheKeyFn

CacheKeyFn computes a cache key after upstream dependencies complete.

```
type CacheKeyFn func(ctx context.Context) CacheKey
```


### type CacheOption

CacheOption tunes a JobNode.Cache declaration.

```
type CacheOption func(*CacheConfig)
```

- `func TTL(d time.Duration) CacheOption` -- TTL sets how long a node's memoized result remains reusable.

### type Cmd

Cmd is the chainable command builder returned by Bash and Exec.

```
type Cmd struct {
    // contains filtered or unexported fields
}
```

- `func Bash(ctx context.Context, line string) *Cmd` -- Bash starts building a shell command (run via "bash -c").
- `func Exec(ctx context.Context, name string, args ...string) *Cmd` -- Exec starts building an argv command (no shell).
- `func (c *Cmd) Capture() (ExecResult, error)` -- Capture executes the command silently -- no per-line log records, just the exec_start banner.
- `func (c *Cmd) Dir(path string) *Cmd` -- Dir sets the working directory for the command.
- `func (c *Cmd) Env(key, value string) *Cmd` -- Env adds (or overrides) a single environment variable.
- `func (c *Cmd) EnvMap(env map[string]string) *Cmd` -- EnvMap merges a map of environment variables into the command.
- `func (c *Cmd) JSON(out any) error` -- JSON runs the command silently and decodes stdout into out via encoding/json.
- `func (c *Cmd) Lines() ([]string, error)` -- Lines runs the command silently and returns stdout split on "\n", with each line trimmed and blanks dropped.
- `func (c *Cmd) MustBeEmpty(reason string) error` -- MustBeEmpty runs the command silently and returns nil only if its stdout (after TrimSpace) is empty.
- `func (c *Cmd) Run() (ExecResult, error)` -- Run executes the command, streaming stdout/stderr line-by-line to the logger installed in ctx.
- `func (c *Cmd) String() (string, error)` -- String runs the command silently and returns TrimSpace(stdout).

### type ConcurrencyGroup

ConcurrencyGroup is a named budget that member nodes share.

```
type ConcurrencyGroup struct {
    // contains filtered or unexported fields
}
```

- `func NewConcurrencyGroup(name string, limit ConcurrencyLimit) *ConcurrencyGroup` -- NewConcurrencyGroup constructs a ConcurrencyGroup named name with the given limit.
- `func (g *ConcurrencyGroup) Limit() ConcurrencyLimit` -- Limit returns the group's declared budget.
- `func (g *ConcurrencyGroup) Name() string` -- Name returns the group's coordination key.

### type ConcurrencyLimit

ConcurrencyLimit is the budget a ConcurrencyGroup enforces.

```
type ConcurrencyLimit struct {
    // Capacity is the total budget in author-defined units. With the
    // default member cost of 1 it reads as "max members running at
    // once". Values <= 0 are treated as 1 by the coordination backend.
    //
    // When two live participants declare different capacities for the
    // same group (a version skew across runs), the effective capacity
    // is the minimum -- a cap is a safety constraint, so lowering takes
    // effect immediately and raising waits for the lower declaration to
    // drain.
    Capacity int
    // Scope is how far the budget reaches (see [Scope]). The zero value
    // is [ScopeGlobal].
    Scope Scope
    // HostAdmission marks a plan-level ScopeBox group as the host execution
    // admission budget for the whole run. ScopeBox alone only says the key
    // is per-machine; HostAdmission says this budget replaces the default
    // host process semaphore while the plan waits for admission.
    HostAdmission bool
    // OnLimit is what a member does when the group is full. The zero
    // value is [Queue].
    OnLimit OnLimit
    // QueueTimeout bounds how long a [Queue] member waits for room
    // before failing with failure_reason "queue_timeout". Zero waits
    // indefinitely. Only meaningful with OnLimit [Queue].
    QueueTimeout time.Duration
    // CancelTimeout bounds how long evicted holders have to cooperatively
    // release before they are force-released, so a stuck victim can't pin
    // the budget indefinitely. Zero uses the backend default. Only
    // meaningful with OnLimit [CancelOthers].
    CancelTimeout time.Duration
}
```


### type Constraint

Constraint is the closed type for per-field declarations in a SchemaBuilder.

```
type Constraint interface {
    // contains filtered or unexported methods
}
```

- `func Bind(argName string) Constraint` -- Bind ties this struct field to a schema-bearing YAML arg key.
- `func Computed(fn any) Constraint` -- Computed defines a default that depends on other (already-resolved) args.
- `func Custom(fn any) Constraint` -- Custom is the escape hatch for validators that don't fit the declarative vocabulary.
- `func Default(v any) Constraint` -- Default supplies a literal fallback used when no higher-priority source (explicit flag, profile default-args) provides a value.
- `func DependsOn(names ...string) Constraint` -- DependsOn declares an explicit ordering edge: the framework resolves the named args before this one.
- `func Max(v any) Constraint` -- Max sets an upper bound; mirror of Min.
- `func Min(v any) Constraint` -- Min sets a lower bound on the field's resolved value.
- `func OneOf(values ...any) Constraint` -- OneOf restricts the field's resolved value to the supplied set.
- `func Positive() Constraint` -- Positive is sugar for Min(1).
- `func Range(min, max any) Constraint` -- Range is sugar for Min(min)+Max(max).
- `func Required() Constraint` -- Required marks the field as unconditionally required: the resolution chain errors if no source (explicit flag, profile default-args, Default, or Computed) provides a value.
- `func RequiredWhen(p Predicate) Constraint` -- RequiredWhen marks the field required only when the predicate evaluates true at resolution time.

### type ConsumeEdge

ConsumeEdge is a resolved consumer->producer artifact edge: the producer whose artifacts this node stages, and the staging prefix (empty stages at the producer's declared relative paths).

```
type ConsumeEdge struct {
    Producer string
    Into     string
}
```


### type ConsumeOption

ConsumeOption tunes a JobNode.Consumes declaration.

```
type ConsumeOption func(*consumeEdge)
```

- `func Into(prefix string) ConsumeOption` -- Into stages the consumed producer's artifacts under prefix, with the producer's internal structure preserved.

### type Dep

Dep is the closed type set accepted by Plan-layer Needs and NeedsOptional.

```
type Dep interface {
    // contains filtered or unexported methods
}
```


### type DescribeArg

DescribeArg is one CLI-visible argument.

```
type DescribeArg struct {
    Name     string   `json:"name"`
    GoName   string   `json:"go_name"`
    Short    string   `json:"short,omitempty"`
    Type     string   `json:"type"`
    Required bool     `json:"required"`
    Desc     string   `json:"desc,omitempty"`
    Default  string   `json:"default,omitempty"`
    Enum     []string `json:"enum,omitempty"`
    Secret   bool     `json:"secret,omitempty"`
    // JobID identifies the job that declared this arg when the arg
    // came from a [WithArgs] embedding rather than the pipeline-level
    // [Register] Inputs struct. Empty for pipeline-level args. Used by
    // the help renderer to group "from job X" annotations and by
    // tooling that needs to attribute flags to their source.
    JobID string `json:"job_id,omitempty"`
}
```


### type DescribePipeline

DescribePipeline is one pipeline's CLI-facing schema.

```
type DescribePipeline struct {
    Name     string        `json:"name"`
    Short    string        `json:"short,omitempty"`
    Help     string        `json:"help,omitempty"`
    Examples []Example     `json:"examples,omitempty"`
    Args     []DescribeArg `json:"args"`
    // EnvVars are environment variables the pipeline reads as inputs,
    // declared via the optional [EnvVarDocer] interface. Empty unless
    // the pipeline opts in.
    EnvVars []EnvVarDoc `json:"env_vars,omitempty"`
    // Extra is true when the pipeline's Inputs struct declares a
    // `flag:",extra"` bag; in that mode unknown flags don't error.
    Extra bool `json:"extra,omitempty"`
    // Risks is the sorted, deduplicated union of per-step risk labels
    // declared anywhere in this pipeline's plan. The dispatcher walks
    // this set against --sw-allow so an operator or agent dispatching
    // a pipeline that calls a risk-labeled Step gets a hard refusal
    // until every label is acknowledged (or --sw-dry-run bypasses).
    // Empty when no step declares a risk; the gate stays silent.
    Risks []string `json:"risks,omitempty"`
    // RisksBySteps is the per-step breakdown so a renderer or agent
    // can show "this is the step that will refuse" in the error path.
    // Only populated for pipelines whose Plan() builds successfully
    // without args during --describe; pipelines with required Inputs
    // degrade to the union field above.
    RisksBySteps []DescribeStepRisks `json:"risks_by_step,omitempty"`
}
```


### type DescribeStepRisks

DescribeStepRisks is one row of the per-step risk-label list.

```
type DescribeStepRisks struct {
    NodeID string   `json:"node_id"`
    StepID string   `json:"step_id"`
    Labels []string `json:"labels"`
}
```


### type EnvVarDoc

EnvVarDoc is one declared env var read by a pipeline.

```
type EnvVarDoc struct {
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Default     string `json:"default,omitempty"`
}
```


### type EnvVarDocer

EnvVarDocer is an optional interface a Pipeline can implement to declare environment variables it reads as inputs.

```
type EnvVarDocer interface {
    EnvVars() []EnvVarDoc
}
```


### type Example

Example is a single help-screen invocation.

```
type Example struct {
    Comment string `json:"comment"`
    Command string `json:"command"`
}
```


### type ExampleProvider

ExampleProvider is optionally implemented by pipelines to contribute worked invocations to `sparkwing run <name> --help`.

```
type ExampleProvider interface {
    Examples() []Example
}
```


### type ExecError

ExecError is returned when a command exits non-zero.

```
type ExecError struct {
    Command  string
    Stdout   string
    Stderr   string
    ExitCode int
    Cause    error
}
```

- `func (e *ExecError) Error() string`
- `func (e *ExecError) Unwrap() error`

### type ExecResult

ExecResult is the structured result of a command invocation.

```
type ExecResult struct {
    Command  string
    Stdout   string
    Stderr   string
    ExitCode int
}
```


### type ExpandGenerator

ExpandGenerator is the closure signature for ExpandFrom.

```
type ExpandGenerator func(ctx context.Context) []*JobNode
```


### type Expansion

Expansion ties a source node to its generator and resulting group.

```
type Expansion struct {
    Source *JobNode
    Group  *JobGroup
    Gen    ExpandGenerator
}
```


### type Failure

Failure describes why a node terminated unsuccessfully.

```
type Failure struct {
    // Stage is the lifecycle stage that produced the failure.
    Stage FailureStage
    // Err is the underlying error: the action error for StageAction, or
    // the verification error for StageVerify.
    Err error
}
```

- `func FailureFromContext(ctx context.Context) Failure` -- FailureFromContext returns the Failure installed by WithFailure, or the zero Failure (StageAction, nil Err) when none is present.

### type FailureRecoveryFn

FailureRecoveryFn is the failure-aware recovery shape JobNode.OnFailure accepts in addition to a Workable or a func(ctx) error.

```
type FailureRecoveryFn func(ctx context.Context, f Failure) error
```


### type FailureStage

FailureStage identifies which lifecycle stage produced a node failure.

```
type FailureStage int
```

- `func (s FailureStage) String() string` -- String returns "action" or "verify".

### type FieldBuilder

FieldBuilder is the chainable handle returned by SchemaBuilder.Field.

```
type FieldBuilder[T any] struct {
    // contains filtered or unexported fields
}
```

- `func (fb *FieldBuilder[T]) Bind(argName string) *FieldBuilder[T]` -- Bind ties this field to a schema-bearing YAML arg key.
- `func (fb *FieldBuilder[T]) Computed(fn any) *FieldBuilder[T]` -- Computed supplies a function-based default that may depend on other (already-resolved) args.
- `func (fb *FieldBuilder[T]) Custom(fn any) *FieldBuilder[T]` -- Custom is the escape-hatch validator (func(T) error).
- `func (fb *FieldBuilder[T]) Default(v any) *FieldBuilder[T]` -- Default supplies a literal fallback value.
- `func (fb *FieldBuilder[T]) DependsOn(names ...string) *FieldBuilder[T]` -- DependsOn declares ordering edges to upstream args.
- `func (fb *FieldBuilder[T]) Max(v any) *FieldBuilder[T]` -- Max sets a numeric upper bound.
- `func (fb *FieldBuilder[T]) Min(v any) *FieldBuilder[T]` -- Min sets a numeric lower bound.
- `func (fb *FieldBuilder[T]) OneOf(values ...any) *FieldBuilder[T]` -- OneOf restricts the resolved value to the supplied set.
- `func (fb *FieldBuilder[T]) Positive() *FieldBuilder[T]` -- Positive is sugar for Min(1).
- `func (fb *FieldBuilder[T]) Range(min, max any) *FieldBuilder[T]` -- Range is sugar for Min(min)+Max(max).
- `func (fb *FieldBuilder[T]) Required() *FieldBuilder[T]` -- Required marks the field unconditionally required.
- `func (fb *FieldBuilder[T]) RequiredWhen(p Predicate) *FieldBuilder[T]` -- RequiredWhen marks the field required when the predicate holds.

### type Git

Git is the run-scoped view of a single git working tree.

```
type Git struct {
    SHA           string `json:"sha,omitempty"`            // full 40-char commit
    Branch        string `json:"branch,omitempty"`         // "main", "" when detached
    DefaultBranch string `json:"default_branch,omitempty"` // origin/HEAD target; "" when no remote
    Repo          string `json:"repo,omitempty"`           // "owner/name"
    RepoURL       string `json:"repo_url,omitempty"`       // "git@github.com:owner/name.git"
    // contains filtered or unexported fields
}
```

- `func NewGit(workDir, sha, branch, defaultBranch, repo, repoURL string) *Git` -- NewGit constructs a Git with the supplied data fields and workDir.
- `func NewGitFromTree(ctx context.Context, workDir string) (*Git, error)` -- NewGitFromTree builds a Git by shelling out to inspect workDir.
- `func (g *Git) ChangedFiles(ctx context.Context, since string) ([]string, error)` -- ChangedFiles returns repo-relative paths modified between `since` and HEAD.
- `func (g *Git) FilesetHash(ctx context.Context) (string, error)` -- FilesetHash returns a deterministic hash of every tracked file's content.
- `func (g *Git) IsDirty(ctx context.Context) (bool, error)` -- IsDirty reports whether the working tree has uncommitted changes.
- `func (g *Git) LatestTag(ctx context.Context, prefix string) (string, error)` -- LatestTag returns the highest semver tag with prefix.
- `func (g *Git) Name() string` -- Name returns the bare repo name (the part after the last "/" in Repo).
- `func (g *Git) PushTag(ctx context.Context, tag, message string) error` -- PushTag creates the annotated tag locally and pushes it to origin.
- `func (g *Git) ShortSHA() string` -- ShortSHA returns g.SHA truncated to 12 chars; safe to call on a nil receiver (returns "").
- `func (g *Git) TagsAtHead(ctx context.Context) ([]string, error)` -- TagsAtHead returns every tag pointing at HEAD.
- `func (g *Git) WorkDir() string` -- WorkDir returns the absolute path of the working tree this Git describes, or "" when constructed without a real clone.

### type GroupBuilder

GroupBuilder is the chainable handle returned by SchemaBuilder.Group.

```
type GroupBuilder struct {
    // contains filtered or unexported fields
}
```

- `func (g *GroupBuilder) AllOrNone() *GroupBuilder` -- AllOrNone requires the group's fields to be uniformly set or uniformly unset.
- `func (g *GroupBuilder) AtLeastOne() *GroupBuilder` -- AtLeastOne requires at least one of the group's fields to be set.
- `func (g *GroupBuilder) AtMostOne() *GroupBuilder` -- AtMostOne forbids more than one of the group's fields being set.
- `func (g *GroupBuilder) Desc(msg string) *GroupBuilder` -- Desc overrides the auto-generated violation message.
- `func (g *GroupBuilder) ExactlyOne() *GroupBuilder` -- ExactlyOne requires exactly one of the group's fields to be set after resolution.
- `func (g *GroupBuilder) When(p Predicate) *GroupBuilder` -- When gates the group's activation.

### type HelpProvider

HelpProvider is optionally implemented by pipelines to contribute a short description to `sparkwing run <name> --help`.

```
type HelpProvider interface {
    Help() string
}
```


### type InputField

InputField is one pipeline-flag description, parsed once at registration time.

```
type InputField struct {
    Name        string // flag name (no `--` prefix)
    Short       string // optional one-letter alias
    GoName      string // original Go field name
    Type        string // "string" | "bool" | "int" | "int64" | "float64" | "duration" | "[]string"
    Default     string // raw default value as written in the tag
    Description string
    Required    bool
    Secret      bool     // mask in logs / dashboard
    Enum        []string // allowed values; empty means unconstrained
    // contains filtered or unexported fields
}
```

- `func (f InputField) IsExtraBag() bool` -- IsExtraBag reports whether this field is the catch-all "extra" map[string]string bag (used to collect unknown args).

### type InputSchema

InputSchema is the resolved description of a pipeline's Inputs struct: one InputField per declared flag, plus a flag indicating whether a `flag:",extra"` bag field is present.

```
type InputSchema struct {
    Fields []InputField
    Extra  bool
}
```


### type JobGroup

JobGroup is a handle to a set of nodes.

```
type JobGroup struct {
    // contains filtered or unexported fields
}
```

- `func GroupJobs(p *Plan, name string, nodes ...*JobNode) *JobGroup` -- GroupJobs declares a named bundle of existing Plan nodes.
- `func JobFanOut[T any](p *Plan, name string, items []T, fn func(T) (string, any)) *JobGroup` -- JobFanOut is the Plan-time static fan-out helper.
- `func JobFanOutDynamic[T any](p *Plan, name string, source *JobNode, fn func(T) (string, any)) *JobGroup` -- JobFanOutDynamic is the runtime fan-out helper.
- `func (g *JobGroup) AfterRun(fn AfterRunFn) *JobGroup` -- AfterRun registers a post-run hook on every member.
- `func (g *JobGroup) BeforeRun(fn BeforeRunFn) *JobGroup` -- BeforeRun registers a pre-run hook on every member.
- `func (g *JobGroup) Cache(key CacheKeyFn, opts ...CacheOption) *JobGroup` -- Cache memoizes every member of the group on content.
- `func (g *JobGroup) Concurrency(cg *ConcurrencyGroup, cost ...int) *JobGroup` -- Concurrency enrolls every member of the group in concurrency group g with the given admission cost.
- `func (g *JobGroup) Consumes(producer *JobNode, opts ...ConsumeOption) *JobGroup` -- Consumes stages the given producer's artifacts into every member's workspace before it runs, and implies Needs(producer) on each.
- `func (g *JobGroup) ContinueOnError() *JobGroup` -- ContinueOnError marks every member so downstream dependents proceed even on failure.
- `func (g *JobGroup) Dynamic() bool` -- Dynamic reports whether this group's membership is determined at dispatch-time (ExpandFrom) rather than plan-construction (GroupJobs).
- `func (g *JobGroup) Env(key, value string) *JobGroup` -- Env sets a per-node environment variable on every member.
- `func (g *JobGroup) Err() error` -- Err returns the expansion error, if any.
- `func (g *JobGroup) Inline() *JobGroup` -- Inline marks every member for in-process execution.
- `func (g *JobGroup) Members() []*JobNode` -- Members returns the group's current nodes.
- `func (g *JobGroup) Name() string` -- Name returns the group's declared name, or "" for an unnamed (structural-only) group.
- `func (g *JobGroup) Needs(deps ...Dep) *JobGroup` -- Needs declares an upstream dependency on every member of the group.
- `func (g *JobGroup) NeedsOptional(deps ...Dep) *JobGroup` -- NeedsOptional declares optional upstream dependencies on every member; unknown IDs are silently dropped at finalize.
- `func (g *JobGroup) Optional() *JobGroup` -- Optional marks every member as non-essential.
- `func (g *JobGroup) Outputs(globs ...string) *JobGroup` -- Outputs declares the same artifact output globs on every member.
- `func (g *JobGroup) Prefers(labels ...string) *JobGroup` -- Prefers biases runner selection for every member.
- `func (g *JobGroup) Ready() <-chan struct{}` -- Ready returns a channel that closes once a dynamic group's expansion completes (success or failure).
- `func (g *JobGroup) Requires(labels ...string) *JobGroup` -- Requires restricts every member to runners advertising the given labels.
- `func (g *JobGroup) Retry(attempts int, opts ...RetryOption) *JobGroup` -- Retry configures every member to be re-attempted up to attempts additional times on failure.
- `func (g *JobGroup) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobGroup` -- SkipIf registers a predicate on every member.
- `func (g *JobGroup) Timeout(d time.Duration) *JobGroup` -- Timeout caps the per-attempt duration on every member.
- `func (g *JobGroup) Verify(fn VerifyFn) *JobGroup` -- Verify registers the same postcondition check on every member.
- `func (g *JobGroup) WhenRunner(labels ...string) *JobGroup` -- WhenRunner marks every member as conditional on the dispatching runner advertising the listed labels.

### type JobNode

JobNode is a single entry in a Plan.

```
type JobNode struct {
    // contains filtered or unexported fields
}
```

- `func Job(p *Plan, id string, x any) *JobNode` -- Job registers a Workable (or a bare func(ctx) error closure) under id and returns the node handle for further configuration (Needs, Env, etc.).
- `func NewDetachedNode(id string, job Workable) *JobNode` -- NewDetachedNode builds a node with full Job-equivalent validation but does not register it on a Plan.
- `func (n *JobNode) AfterRun(fn AfterRunFn) *JobNode` -- AfterRun registers a hook to run once after Run terminates, including after all retries.
- `func (n *JobNode) AfterRunHooks() []AfterRunFn` -- AfterRunHooks returns the node's registered post-run hooks.
- `func (n *JobNode) ApprovalConfig() *ApprovalConfig` -- ApprovalConfig returns the per-node approval configuration, or nil for non-approval nodes.
- `func (n *JobNode) BeforeRun(fn BeforeRunFn) *JobNode` -- BeforeRun registers a hook to run once before the node's Run method on the first attempt.
- `func (n *JobNode) BeforeRunHooks() []BeforeRunFn` -- BeforeRunHooks returns the node's registered pre-run hooks.
- `func (n *JobNode) Cache(key CacheKeyFn, opts ...CacheOption) *JobNode` -- Cache memoizes the node's result on content.
- `func (n *JobNode) CacheConfig() *CacheConfig` -- CacheConfig returns the node's resolved content-cache configuration, or nil when JobNode.Cache was not called.
- `func (n *JobNode) Concurrency(g *ConcurrencyGroup, cost ...int) *JobNode` -- Concurrency enrolls the node in concurrency group g with the given admission cost (default 1).
- `func (n *JobNode) ConcurrencyCost() int` -- ConcurrencyCost returns the admission cost declared via JobNode.Concurrency, or 0 when the node has no membership.
- `func (n *JobNode) ConcurrencyGroupRef() *ConcurrencyGroup` -- ConcurrencyGroupRef returns the group the node joined via JobNode.Concurrency, or nil when the node declared no membership.
- `func (n *JobNode) ConsumeEdges() []ConsumeEdge` -- ConsumeEdges returns the artifact edges declared via Consumes, in declaration order, or nil if the node consumes nothing.
- `func (n *JobNode) Consumes(producer *JobNode, opts ...ConsumeOption) *JobNode` -- Consumes declares that this node stages the artifacts produced by producer into its workspace before it runs, and implies Needs(producer).
- `func (n *JobNode) ContinueOnError() *JobNode` -- ContinueOnError tells the orchestrator that downstream dependents should proceed even when this node fails.
- `func (n *JobNode) DepIDs() []string` -- DepIDs returns the node IDs this node depends on.
- `func (n *JobNode) Env(key, value string) *JobNode` -- Env sets a per-node environment variable.
- `func (n *JobNode) EnvMap() map[string]string` -- EnvMap returns the node's declared environment.
- `func (n *JobNode) ID() string` -- ID returns the node's identifier.
- `func (n *JobNode) Inline() *JobNode` -- Inline marks the node for in-process execution on the dispatcher, bypassing the configured Runner.
- `func (n *JobNode) IsApproval() bool` -- IsApproval reports whether the node is an approval gate.
- `func (n *JobNode) IsContinueOnError() bool` -- IsContinueOnError reports whether downstream should ignore this node's failure for dispatch purposes.
- `func (n *JobNode) IsInline() bool` -- IsInline reports whether the node was marked for orchestrator-local execution via Inline().
- `func (n *JobNode) IsOptional() bool` -- IsOptional reports whether the node is marked non-essential.
- `func (n *JobNode) Job() Workable` -- Job returns the underlying user-authored job struct.
- `func (n *JobNode) Needs(deps ...Dep) *JobNode` -- Needs declares hard upstream dependencies.
- `func (n *JobNode) NeedsGroups() []*JobGroup` -- NeedsGroups returns any dynamic groups (from ExpandFrom) this node is waiting on.
- `func (n *JobNode) NeedsOptional(deps ...Dep) *JobNode` -- NeedsOptional declares upstream dependencies that may or may not be present in the plan.
- `func (n *JobNode) OnFailure(id string, x any) *JobNode` -- OnFailure registers a recovery node that runs only when this node terminates with outcome=failed; otherwise it's marked Skipped.
- `func (n *JobNode) OnFailureNode() *JobNode` -- OnFailureNode returns the recovery node registered via OnFailure, or nil if none.
- `func (n *JobNode) Optional() *JobNode` -- Optional marks the node as non-essential: a failure is logged as a warning and does not count toward the run's overall success/fail outcome.
- `func (n *JobNode) OptionalDepIDs() []string` -- OptionalDepIDs returns the IDs declared via NeedsOptional.
- `func (n *JobNode) OutputGlobs() []string` -- OutputGlobs returns the artifact output globs declared via Outputs (the union across calls), or nil if the node declared none.
- `func (n *JobNode) OutputType() reflect.Type` -- OutputType returns the concrete Go type of the job's Run output, or nil if the job's Run returns no value beyond error.
- `func (n *JobNode) Outputs(globs ...string) *JobNode` -- Outputs declares the files this node emits as artifacts, by glob, relative to its working directory.
- `func (n *JobNode) Prefers(labels ...string) *JobNode` -- Prefers biases runner selection when more than one runner satisfies Requires.
- `func (n *JobNode) PrefersLabels() []string` -- PrefersLabels returns the terms declared via Prefers.
- `func (n *JobNode) Requires(labels ...string) *JobNode` -- Requires restricts this job to runners advertising every term in the given set.
- `func (n *JobNode) RequiresLabels() []string` -- RequiresLabels returns the terms declared via Requires.
- `func (n *JobNode) ResultStep() *WorkStep` -- ResultStep returns the *WorkStep the Job designated as its typed output via Work's return value, or nil for untyped Jobs.
- `func (n *JobNode) Retry(attempts int, opts ...RetryOption) *JobNode` -- Retry configures the node to be re-attempted up to attempts additional times on failure.
- `func (n *JobNode) RetryConfig() RetryConfig` -- RetryConfig returns the resolved retry envelope.
- `func (n *JobNode) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobNode` -- SkipIf registers a predicate the orchestrator evaluates after this node's dependencies complete.
- `func (n *JobNode) SkipIfBudget() time.Duration` -- SkipIfBudget returns the configured per-predicate evaluation budget, or zero for the orchestrator's default.
- `func (n *JobNode) SkipPredicates() []SkipPredicate` -- SkipPredicates returns the node's registered skip predicates.
- `func (n *JobNode) Timeout(d time.Duration) *JobNode` -- Timeout caps the per-attempt duration.
- `func (n *JobNode) TimeoutDuration() time.Duration` -- TimeoutDuration returns the configured per-attempt timeout, or zero if unlimited.
- `func (n *JobNode) Verifier() VerifyFn` -- Verifier returns the node's Verify postcondition, or nil if none.
- `func (n *JobNode) Verify(fn VerifyFn) *JobNode` -- Verify registers a postcondition checked after the node's action succeeds.
- `func (n *JobNode) WhenRunner(labels ...string) *JobNode` -- WhenRunner marks the job as conditional on the dispatching runner advertising the listed labels (same comma-OR / AND semantics as Requires).
- `func (n *JobNode) WhenRunnerLabels() []string` -- WhenRunnerLabels returns the terms declared via WhenRunner.
- `func (n *JobNode) Work() *Work` -- Work returns the materialized inner DAG for the node's job.

### type LintWarning

LintWarning is a non-fatal Plan-time advisory attached to a node.

```
type LintWarning struct {
    NodeID string
    Code   string // short stable identifier, e.g. "node-stale-cache"
    Msg    string
}
```


### type LogRecord

LogRecord is the structured unit every Logger receives.

```
type LogRecord struct {
    TS    time.Time      `json:"ts"`
    Level string         `json:"level,omitempty"` // "info" | "warn" | "error"
    JobID string         `json:"node,omitempty"`  // set by jobLogger on writes to disk + delegate; wire tag stays "node" for log-format compat
    Step  string         `json:"step,omitempty"`  // active step ID, set by recordEnvelope inside the step body
    Event string         `json:"event,omitempty"` // "" (plain msg), "node_start", "node_end", "node_annotation", "node_summary", "step_start", "step_end", "step_skipped", "retry", "exec_line", "run_plan", "run_summary", "run_finish"
    Msg   string         `json:"msg,omitempty"`
    Attrs map[string]any `json:"attrs,omitempty"`
}
```


### type Logger

Logger is the sink for job output.

```
type Logger interface {
    Log(level, msg string)
    Emit(rec LogRecord)
}
```

- `func LoggerFromContext(ctx context.Context) Logger` -- LoggerFromContext returns the active logger or a no-op if none is set.

### type Logs

Logs is the per-job log stream store the orchestrator and pipeline authors reach for.

```
type Logs = storage.LogStore
```


### type NoInputs

NoInputs is the empty-struct convention for pipelines that take no flags.

```
type NoInputs struct{}
```


### type OnLimit

OnLimit is the closed set of behaviors for a node that finds its ConcurrencyGroup at capacity.

```
type OnLimit string
```


### type Outcome

Outcome is the terminal state of a node in a Plan run.

```
type Outcome string
```

- `func (o Outcome) OK() bool` -- OK reports whether the outcome satisfies downstream dependencies.
- `func (o Outcome) Terminal() bool` -- Terminal reports whether the outcome ends the node's lifecycle.

### type Pipeline

Pipeline is the canonical pipeline shape: every pipeline declares a typed Inputs struct and populates a Plan that the SDK constructs and passes in.

```
type Pipeline[T any] interface {
    Plan(ctx context.Context, plan *Plan, in T, rc RunContext) error
}
```


### type PipelineAwaiter

PipelineAwaiter is the orchestrator-installed backend for RunAndAwait.

```
type PipelineAwaiter interface {
    Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)
}
```


### type PipelineAwaiterFunc

PipelineAwaiterFunc adapts a plain function to PipelineAwaiter.

```
type PipelineAwaiterFunc func(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)
```

- `func (f PipelineAwaiterFunc) Await(ctx context.Context, req AwaitRequest) (*ResolvedPipelineRef, error)`

### type PipelineResolver

PipelineResolver is the backend-facing interface installed on ctx.

```
type PipelineResolver interface {
    // contains filtered or unexported methods
}
```


### type PipelineResolverFunc

PipelineResolverFunc adapts a plain function to PipelineResolver.

```
type PipelineResolverFunc func(ctx context.Context, pipeline, nodeID string, maxAge time.Duration) (*ResolvedPipelineRef, error)
```


### type Plan

Plan is the typed DAG a pipeline returns from its Plan method.

```
type Plan struct {
    // contains filtered or unexported fields
}
```

- `func NewPlan() *Plan` -- NewPlan returns an empty Plan.
- `func (p *Plan) Concurrency(g *ConcurrencyGroup, cost ...int) *Plan` -- Concurrency gates the whole run on concurrency group g: the run acquires each declared plan-level budget before any node dispatches and releases it when the run reaches a terminal status.
- `func (p *Plan) ConcurrencyCost() int` -- ConcurrencyCost returns the first plan-level admission cost declared via Plan.Concurrency, or 0 when the plan declared no whole-run coordination.
- `func (p *Plan) ConcurrencyGroupRef() *ConcurrencyGroup` -- ConcurrencyGroupRef returns the first group set via Plan.Concurrency, or nil when the plan declared no whole-run coordination.
- `func (p *Plan) Expansions() []Expansion` -- Expansions returns the registered ExpandFrom generators.
- `func (p *Plan) GroupSourceIDs(id string) []string` -- GroupSourceIDs returns the ids of the source nodes backing any ExpandFrom Groups this node waits on via Needs(group).
- `func (p *Plan) HostAdmission() bool` -- HostAdmission reports whether the plan-level concurrency group owns host execution admission for this run.
- `func (p *Plan) Inputs() any` -- Inputs returns the parsed Inputs value the orchestrator handed to this pipeline's Plan() method, or nil for a Plan built directly (outside the registration path).
- `func (p *Plan) IsDynamicNode(id string) bool` -- IsDynamicNode reports whether the node sources runtime-variable downstream work -- i.e.
- `func (p *Plan) Job(id string) *JobNode` -- Job returns the node with the given ID, or nil if absent.
- `func (p *Plan) JobArgSchema(id string) *Schema` -- JobArgSchema returns the args schema for the named job, or nil when that job doesn't declare typed args.
- `func (p *Plan) JobArgSchemas() map[string]*Schema` -- JobArgSchemas returns every job-args schema registered against this plan, keyed by node id.
- `func (p *Plan) JobGroupNames(id string) []string` -- JobGroupNames returns the names of every declared *JobGroup whose members include the given node.
- `func (p *Plan) LintWarnings() []LintWarning` -- LintWarnings returns the non-fatal Plan-time advisories accumulated while building this Plan.
- `func (p *Plan) Nodes() []*JobNode` -- Nodes returns the plan's nodes in insertion order.
- `func (p *Plan) PlanConcurrency() []PlanConcurrency` -- PlanConcurrency returns every whole-run gate declared via Plan.Concurrency.
- `func (p *Plan) ResolvedArgs() map[string]any` -- ResolvedArgs returns the merged map of every job's typed-args resolution result, keyed by CLI flag name.
- `func (p *Plan) TransitiveArgsSurface() map[string]TransitiveArg` -- TransitiveArgsSurface returns the deduplicated map of every flag the plan exposes (across all its jobs that declare args), keyed by flag name with the owning job id.

### type PlanConcurrency

PlanConcurrency records one whole-run concurrency gate.

```
type PlanConcurrency struct {
    Group *ConcurrencyGroup
    Cost  int
}
```


### type Predicate

Predicate is the closed condition language used by the args resolution chain: RequiredWhen, group .When(), default-when, etc.

```
type Predicate interface {
    // Eval returns true when the condition holds against the current
    // resolution context.
    Eval(ctx PredicateContext) bool
    // String returns a deterministic human-readable rendering for
    // error messages and the `pipeline describe --args` view. Avoid
    // embedding values that change run-to-run.
    String() string
    // contains filtered or unexported methods
}
```

- `func Always() Predicate` -- Always holds unconditionally.
- `func And(preds ...Predicate) Predicate` -- And holds when every nested predicate holds.
- `func ArgEq(name string, value any) Predicate` -- ArgEq holds when the named arg's resolved value equals value.
- `func ArgIn(name string, values ...any) Predicate` -- ArgIn holds when the named arg's resolved value matches any of the supplied values.
- `func ArgNeq(name string, value any) Predicate` -- ArgNeq holds when the named arg has a resolved value AND that value is not equal to value.
- `func ArgSet(name string) Predicate` -- ArgSet holds when some source (explicit flag, profile default-args, schema default, or computed) has populated the named arg.
- `func ArgUnset(name string) Predicate` -- ArgUnset holds when no source has populated the named arg -- it has no resolved value at all.
- `func Not(p Predicate) Predicate` -- Not inverts the nested predicate.
- `func Or(preds ...Predicate) Predicate` -- Or holds when any nested predicate holds.
- `func Profile(name string) Predicate` -- Profile holds when the named profile is the resolved profile.

### type PredicateContext

PredicateContext is the evaluation environment for a Predicate.

```
type PredicateContext interface {
    // Arg returns the resolved value of the named arg and true when
    // some source provided a value (explicit flag, profile
    // default-args, computed, or a constraint Default). Returns
    // (nil, false) when the arg has no resolved value at all -- this
    // is the signal [ArgUnset] looks for.
    Arg(name string) (value any, ok bool)
    // ProfileName is the resolved profile's name.
    ProfileName() string
    // ProfileIsLocal returns true when the resolved profile has no
    // controller -- the run executes on the operator's machine. Used
    // by [Local] / [Remote].
    ProfileIsLocal() bool
}
```


### type PrefersProvider

PrefersProvider is optionally implemented by a Workable struct to declare its own runner preferences.

```
type PrefersProvider interface {
    Prefers() []string
}
```


### type Produces

Produces is a zero-size marker embedded in a Job to declare its output contract at the type level.

```
type Produces[T any] struct{}
```


### type ProfileResolutionContext

ProfileResolutionContext is the slice of profile state the v0.6 args-resolver needs at registration-invoke time: the active profile name (so Profile("prod") predicates fire) and whether that profile is local-only (so Local / Remote gates resolve correctly).

```
type ProfileResolutionContext struct {
    // Name is the active profile's name (e.g. "prod", "local"), used
    // by the [Profile](name) predicate. Empty when no profile is
    // active.
    Name string

    // IsLocal reports whether the active profile routes through the
    // in-process local SQLite (i.e. the laptop builtin). Drives the
    // [Local] / [Remote] context predicates.
    IsLocal bool
}
```


### type Ref

Ref is a typed reference to another node's output.

```
type Ref[T any] struct {
    // NodeID identifies the producing node. For in-run refs this is
    // a sibling node id; for cross-pipeline refs this is a node id
    // inside Pipeline.
    NodeID string

    // Pipeline names the upstream pipeline when the ref is
    // cross-pipeline. Empty means in-run.
    Pipeline string

    // MaxAge bounds the freshness of a cross-pipeline run lookup.
    // Zero means "any successful run, however old." Ignored for
    // in-run refs.
    MaxAge time.Duration
}
```

- `func RefTo[T any](n *JobNode) Ref[T]` -- RefTo returns a Ref[T] pointing at an in-run node's output.
- `func RefToLastRun[T any](pipeline, nodeID string, opts ...RefOption) Ref[T]` -- RefToLastRun returns a Ref[T] pointing at node nodeID in the most recent successful run of pipeline.
- `func (r Ref[T]) Get(ctx context.Context) T` -- Get resolves the reference to a typed T value.
- `func (r Ref[T]) Job() string` -- Job returns the upstream node id this reference points at.

### type RefOption

RefOption tunes a cross-pipeline ref constructor (RefToLastRun).

```
type RefOption func(*refOpts)
```

- `func MaxAge(d time.Duration) RefOption` -- MaxAge bounds cross-pipeline ref resolution to runs whose finished_at is within d.

### type RefTarget

RefTarget is one (pipeline, node) pair discovered on a job struct via collectCrossPipelineRefs.

```
type RefTarget struct {
    Pipeline string
    NodeID   string
}
```


### type Registration

Registration is the registry's record for one pipeline.

```
type Registration struct {
    // Name is the invocation name (e.g. "lint", "build-test-deploy").
    Name string

    // InputType is the reflect.Type of the pipeline's Inputs struct,
    // retained for introspection. Same struct described by Schema.
    InputType reflect.Type

    // Schema is the resolved input description, parsed once at
    // registration. CLI describe / --help / completion / dashboard
    // run-form / MCP tool definitions all read from Schema.
    Schema InputSchema

    // Invoke is the type-erased entry point: parse the wire-format
    // args map into the typed Inputs struct, instantiate a fresh
    // pipeline, and call its Plan.
    Invoke func(ctx context.Context, args map[string]string, rc RunContext) (*Plan, error)
    // contains filtered or unexported fields
}
```

- `func Lookup(name string) (*Registration, bool)` -- Lookup returns the Registration for a registered pipeline name, or ok=false if none.
- `func (r *Registration) Instance() any` -- Instance returns a fresh pipeline value for this registration, used by introspection helpers that query optional provider interfaces (HelpProvider, ShortHelpProvider, ExampleProvider).
- `func (r *Registration) SecretValues(args map[string]string) []string` -- SecretValues resolves the schema's secret-marked Inputs fields against the wire-format args map (applying tag-declared defaults for unset keys) and returns the resolved string values.

### type RequiresProvider

RequiresProvider is optionally implemented by a Workable struct to declare its own hard runner constraint.

```
type RequiresProvider interface {
    Requires() []string
}
```


### type ResolveInputs

ResolveInputs is everything the resolution chain needs to bind a pipeline's args.

```
type ResolveInputs struct {
    // FlagValues is the parsed CLI flag map keyed by flag name
    // (kebab-cased; matches fieldMeta.Flag). Absence of a key means
    // the user didn't pass --flag on the command line. Values are
    // stored as strings; the resolver converts to the target field's
    // Go type during binding.
    FlagValues map[string]string

    // ProfileName / ProfileIsLocal feed the predicate context used
    // by RequiredWhen and group .When() evaluation. ProfileIsLocal
    // is true when the resolved profile has no controller.
    ProfileName    string
    ProfileIsLocal bool
}
```


### type ResolvedPipelineRef

ResolvedPipelineRef is what a PipelineResolver returns: the source run id (for audit) + raw output JSON (for Ref[T].Get to unmarshal).

```
type ResolvedPipelineRef struct {
    RunID string
    Data  []byte
}
```


### type RetryConfig

RetryConfig is the resolved retry envelope for a Job.

```
type RetryConfig struct {
    Attempts int
    Backoff  time.Duration
    Auto     bool
}
```


### type RetryOption

RetryOption tunes a Retry(...) call.

```
type RetryOption func(*RetryConfig)
```

- `func RetryAuto() RetryOption` -- RetryAuto switches the retry mechanism from in-runner step re-run to whole-node re-dispatch.
- `func RetryBackoff(d time.Duration) RetryOption` -- RetryBackoff sets the initial backoff between retry attempts.

### type RiskBlockedError

RiskBlockedError is the typed error returned when the dispatcher refuses a run because one or more reachable steps declare risk labels the operator hasn't acknowledged via --sw-allow (or --sw-dry-run, which bypasses every gate).

```
type RiskBlockedError struct {
    Pipeline      string
    StepID        string
    MissingLabels []string
}
```

- `func (e *RiskBlockedError) Error() string`

### type RunContext

RunContext is the typed environment every Plan and Job sees.

```
type RunContext struct {
    // RunID uniquely identifies the overall pipeline run.
    RunID string

    // Pipeline is the registered name of the invoked pipeline
    // (e.g. "lint", "build-test-deploy").
    Pipeline string

    // Git is the run's view of the cloned working tree. Same instance
    // as `Runtime().Git`. Live methods (IsDirty, FilesetHash, …) shell
    // out fresh each call; data fields (SHA, Branch, Repo, RepoURL)
    // are the trigger-time snapshot.
    Git *Git

    // Trigger describes how the run was initiated.
    Trigger TriggerInfo

    // StartedAt is set when the orchestrator begins the run.
    StartedAt time.Time
}
```


### type RunnerInfo

RunnerInfo describes the runner that's about to execute (or is executing) the current job.

```
type RunnerInfo struct {
    // Name is the runner identifier as declared in runners.yaml
    // (e.g. "local", "cloud-linux", "mac-mini"). Empty when the
    // active runner hasn't been named -- treat as a synonym for
    // "the implicit runner of this dispatch venue."
    Name string

    // Type is the runner kind. Same vocabulary as runners.yaml's
    // type: field -- "local", "kubernetes", "static". Empty when
    // the orchestrator couldn't classify the active runner.
    Type string

    // Labels are the equality strings the runner advertises.
    // Same shape as runners.yaml's labels: list and runner.LabelAdvertiser
    // AdvertisedLabels.
    Labels []string
}
```

- `func Runner(ctx context.Context) *RunnerInfo` -- Runner returns the RunnerInfo the orchestrator installed for the current job.
- `func (r *RunnerInfo) HasLabel(term string) bool` -- HasLabel reports whether the runner advertises the given label term.

### type RuntimeConfig

RuntimeConfig is the snapshot of "what is true about this process at the moment it started." Populated once at package init by walking up from cwd to find the project root; stable for the lifetime of the run.

```
type RuntimeConfig struct {
    // WorkDir is the directory the pipeline should treat as the
    // repo root. Discovered at process init by walking up from cwd
    // looking for a `.sparkwing/` subdir. Empty when no project
    // was found above cwd; helpers (Path, ReadFile, ...) then
    // refuse to run with a clear error.
    WorkDir string

    // Git describes the source state being built. Same instance
    // as RunContext.Git. Always non-nil so live methods are safe
    // to call from init time; data fields stay empty until SetGit
    // fills them.
    Git *Git
}
```

- `func CurrentRuntime() RuntimeConfig` -- CurrentRuntime returns the RuntimeConfig snapshot for this process.

### type Schema

Schema is the immutable, fully-validated arg metadata produced by SchemaBuilder.Build.

```
type Schema struct {
    // contains filtered or unexported fields
}
```

- `func NewSchemaFromType(t reflect.Type) (*Schema, error)` -- NewSchemaFromType synthesizes a zero-constraint schema from a reflect.Type.
- `func (s *Schema) DescribeArgs() []DescribeArg` -- DescribeArgs projects the schema's fields into the wire-format DescribeArg shape so the describe-cache / --help renderer / tab- completion all see a job's WithArgs[T] fields in the same envelope as pipeline-level Inputs fields.
- `func (s *Schema) Fields() []string` -- Fields returns the field names in resolution order (topo-sorted over DependsOn + inferred Computed edges).
- `func (s *Schema) GoType() reflect.Type` -- GoType returns the args struct type the schema validates against.
- `func (s *Schema) Resolve(in ResolveInputs) (reflect.Value, error)` -- Resolve binds the schema's fields against the supplied inputs and returns a populated reflect.Value of the args struct (kind=Struct, type matches s.GoType).

### type SchemaBuilder

SchemaBuilder is the chainable builder for a job's args schema.

```
type SchemaBuilder[T any] struct {
    // contains filtered or unexported fields
}
```

- `func NewSchema[T any]() *SchemaBuilder[T]` -- NewSchema constructs a fresh SchemaBuilder over the args struct T. The framework constructs one per job at registration time and passes it to the job's Schema(*SchemaBuilder[T]) method.
- `func (sb *SchemaBuilder[T]) Build() (*Schema, error)` -- Build validates the accumulated schema against the args struct T and produces an immutable Schema ready for the resolution chain.
- `func (sb *SchemaBuilder[T]) Field(name string) *FieldBuilder[T]` -- Field returns the FieldBuilder for the named struct field.
- `func (sb *SchemaBuilder[T]) Group(names ...string) *GroupBuilder` -- Group declares a cross-field cardinality rule over the named struct fields.

### type SchemaProvider

SchemaProvider is the optional interface a job implements to declare its typed args' constraints.

```
type SchemaProvider interface {
    Schema() (*Schema, error)
}
```


### type Scope

Scope selects how far a ConcurrencyGroup's budget reaches: only the nodes of one run, every run on one machine, or the whole fleet coordinating through a shared backend.

```
type Scope string
```


### type SecretField

SecretField is one entry in the result of InspectPipelineSecrets: the declared secret + (when a SecretResolver is installed on ctx) the resolution outcome.

```
type SecretField struct {
    // Name is the secret name as the pipeline asks for it.
    Name string
    // GoField, when non-empty, is the Go struct field on Secrets()
    // that maps to this secret name. Empty for secrets declared
    // only in sparkwing.yaml secrets: list.
    GoField string
    // Required reports whether the declaration marked this secret
    // required. Required secrets fail the run at fail-fast time
    // when the resolver can't find them.
    Required bool
    // DeclaredIn is "sparkwing.yaml secrets:" when the secret came
    // from the yaml list, "Secrets() struct" when it came from the
    // pipeline's typed Secrets struct.
    DeclaredIn string
    // Resolved reports whether the resolver returned a value.
    // Set when a SecretResolver was installed on ctx; left at the
    // zero value (false) when inspection ran without one.
    Resolved bool
    // Note carries the resolution error message (or "not resolved
    // yet" when no resolver is installed). Empty when Resolved is
    // true and no error occurred.
    Note string
}
```

- `func InspectPipelineSecrets(ctx context.Context, reg *Registration, yamlEntry *pipelines.Pipeline) ([]SecretField, error)` -- InspectPipelineSecrets enumerates the pipeline's declared secrets and (when ctx carries a SecretResolver) attempts each one.

### type SecretResolver

SecretResolver resolves a stored value to (plain, masked) at the moment of the call.

```
type SecretResolver interface {
    Resolve(ctx context.Context, name string) (value string, masked bool, err error)
}
```

- `func NewSecretResolverFromSpec(_ context.Context, spec backends.Spec) (SecretResolver, error)` -- NewSecretResolverFromSpec builds a SecretResolver for the secrets surface from a backends.Spec.

### type SecretResolverFunc

SecretResolverFunc adapts a function to SecretResolver.

```
type SecretResolverFunc func(ctx context.Context, name string) (value string, masked bool, err error)
```

- `func (f SecretResolverFunc) Resolve(ctx context.Context, name string) (string, bool, error)` -- Resolve satisfies SecretResolver.

### type SecretsProvider

SecretsProvider is optionally implemented by a pipeline value to declare its typed secrets struct.

```
type SecretsProvider interface {
    Secrets() any
}
```


### type ShortHelpProvider

ShortHelpProvider is optionally implemented by pipelines to contribute a one-line hint (<=80 chars, no trailing period) for tab completion and list views.

```
type ShortHelpProvider interface {
    ShortHelp() string
}
```


### type SkipOption

SkipOption configures a SkipIf registration.

```
type SkipOption func(*JobNode)
```

- `func SkipBudget(d time.Duration) SkipOption` -- SkipBudget overrides the per-predicate evaluation budget.

### type SkipPredicate

SkipPredicate is a function evaluated by the orchestrator after upstream dependencies complete.

```
type SkipPredicate func(ctx context.Context) bool
```


### type SparkwingFlagDoc

SparkwingFlagDoc is a public, single-source-of-truth description of one sparkwing-owned flag.

```
type SparkwingFlagDoc struct {
    // Name is the long flag name without the leading "--".
    Name string
    // Short is the optional one-letter alias without the leading "-"
    // (e.g. "v" for --sw-verbose). Empty for flags without a short form.
    Short string
    // Argument is the value placeholder for value-taking flags
    // (e.g. "PATH", "REF"); empty for boolean flags.
    Argument string
    // Desc is the one-line help text shown in --help output.
    Desc string
    // Group is the rendering bucket: currently a single "System"
    // label. Per-pipeline help uses this to section the footer;
    // `sparkwing run --help` uses it via FlagSpec.Group.
    Group string
    // Hot marks flags an operator reaches for on most runs. Default
    // --help and tab-completion menus filter to Hot=true entries to
    // keep the surface small; the long tail surfaces via --help-all.
    Hot bool
}
```

- `func SparkwingFlagDocs() []SparkwingFlagDoc` -- SparkwingFlagDocs returns the canonical sparkwing-owned flag documentation.

### type SpawnGenSpec

SpawnGenSpec is the static record of a JobSpawnEach declaration.

```
type SpawnGenSpec struct {
    // contains filtered or unexported fields
}
```

- `func JobSpawnEach(w *Work, items, fn any) *SpawnGenSpec` -- JobSpawnEach is the cardinality-many variant of JobSpawn.
- `func (g *SpawnGenSpec) DepIDs() []string` -- DepIDs returns the WorkStep IDs the generator waits on.
- `func (g *SpawnGenSpec) Fn() any` -- Fn returns the per-item closure.
- `func (g *SpawnGenSpec) ID() string` -- ID exposes the synthetic id (e.g.
- `func (g *SpawnGenSpec) Items() any` -- Items returns the input slice value.
- `func (g *SpawnGenSpec) Needs(deps ...WorkDep) *SpawnGenSpec` -- Needs declares which Steps / Spawns must complete before the generator runs.

### type SpawnHandler

SpawnHandler is the orchestrator-provided callback that fires a SpawnNode declaration from inside an executing Work.

```
type SpawnHandler interface {
    Spawn(ctx context.Context, parentNodeID, spawnID string, job Workable) (output any, err error)
}
```


### type SpawnHandlerFunc

SpawnHandlerFunc adapts a closure into a SpawnHandler.

```
type SpawnHandlerFunc func(ctx context.Context, parentNodeID, spawnID string, job Workable) (any, error)
```

- `func (f SpawnHandlerFunc) Spawn(ctx context.Context, parentNodeID, spawnID string, job Workable) (any, error)` -- Spawn implements SpawnHandler.

### type SpawnSpec

SpawnSpec is the static record of a JobSpawn declaration.

```
type SpawnSpec struct {
    // contains filtered or unexported fields
}
```

- `func JobSpawn(w *Work, id string, x any) *SpawnSpec` -- JobSpawn dispatches a registered Job as a fresh Plan node from inside a Work.
- `func (s *SpawnSpec) DepIDs() []string` -- DepIDs returns WorkStep IDs the spawn waits on inside its parent Work.
- `func (s *SpawnSpec) ID() string` -- ID returns the spawn's local id (not the eventual Plan node id, which is namespaced by the spawning Job).
- `func (s *SpawnSpec) Job() Workable` -- Job returns the spawn's target.
- `func (s *SpawnSpec) Needs(deps ...WorkDep) *SpawnSpec` -- Needs declares which Steps / Spawns inside the same Work must complete before the spawn fires.
- `func (s *SpawnSpec) ResolvedID() string` -- ResolvedID returns the assigned Plan node id, populated after the spawn fires.
- `func (s *SpawnSpec) SkipIf(fn SkipPredicate) *SpawnSpec` -- SkipIf registers a predicate the orchestrator evaluates before firing the spawn.
- `func (s *SpawnSpec) SkipPredicates() []SkipPredicate` -- SkipPredicates returns the spawn's registered predicates.

### type State

State is the run-record store: persists runs, nodes, steps, annotations, approvals, and the schema migrations the orchestrator depends on.

```
type State = storage.StateStore
```


### type StepError

StepError wraps a step body's error with the originating step ID.

```
type StepError struct {
    StepID string
    Cause  error
}
```

- `func (e *StepError) Error() string`
- `func (e *StepError) Unwrap() error`

### type StepGroup

StepGroup is a handle to a named group of Steps.

```
type StepGroup struct {
    // contains filtered or unexported fields
}
```

- `func GroupSteps(w *Work, name string, steps ...*WorkStep) *StepGroup` -- GroupSteps declares a named bundle of Work steps.
- `func (g *StepGroup) Members() []*WorkStep` -- Members returns the group's steps.
- `func (g *StepGroup) Name() string` -- Name returns the group's declared name.
- `func (g *StepGroup) Needs(deps ...WorkDep) *StepGroup` -- Needs declares an upstream dependency on every member of the group.
- `func (g *StepGroup) SkipIf(fn SkipPredicate) *StepGroup` -- SkipIf registers a predicate on every member of the group.

### type TransitiveArg

TransitiveArg is one entry in Plan.TransitiveArgsSurface: the flag, the job that owns it, the underlying Go field, and the schema for resolving its value.

```
type TransitiveArg struct {
    Flag      string
    JobID     string
    FieldName string
    Desc      string
    Schema    *Schema
}
```


### type TriggerInfo

TriggerInfo describes the trigger that started the run.

```
type TriggerInfo struct {
    Source string // "manual", "push", "schedule", "webhook"
    User   string // invoker identity, when known
}
```


### type VerifyError

VerifyError wraps the error returned by a node's Verify check.

```
type VerifyError struct{ Err error }
```

- `func (e *VerifyError) Error() string`
- `func (e *VerifyError) Unwrap() error`

### type VerifyFn

VerifyFn is a postcondition checked after a node's action succeeds.

```
type VerifyFn func(ctx context.Context) error
```


### type WhenRunnerProvider

WhenRunnerProvider is optionally implemented by a Workable struct to declare its own dispatch-time eligibility labels.

```
type WhenRunnerProvider interface {
    WhenRunner() []string
}
```


### type WithArgs

WithArgs is the embedded helper a job uses to declare a typed Args struct.

```
type WithArgs[T any] struct {
    // contains filtered or unexported fields
}
```

- `func (w *WithArgs[T]) Args(ctx context.Context) T` -- Args returns the resolved typed args for the current run.
- `func (w *WithArgs[T]) ArgsType() reflect.Type` -- ArgsType returns the reflect.Type of T. Used by the framework's schema-discovery pass: given a job that embeds WithArgs[T], the framework calls ArgsType on the embedded value to learn T without having to peek at the unexported bound field.
- `func (w *WithArgs[T]) BindFromAny(val any) error` -- BindFromAny is called by the framework to populate the resolved args struct.

### type Work

Work is the inner DAG of a Job.

```
type Work struct {
    // contains filtered or unexported fields
}
```

- `func NewWork() *Work` -- NewWork returns an empty Work.
- `func (w *Work) Groups() []*StepGroup` -- Groups returns the StepGroups declared on this Work in declaration order.
- `func (w *Work) PreviewSkipForRange(startAt, stopAt string) map[string]string` -- PreviewSkipForRange computes the (id -> human-readable reason) skip set this Work would apply under the given --start-at / --stop-at bounds, WITHOUT executing any step body.
- `func (w *Work) SpawnGens() []*SpawnGenSpec` -- SpawnGens returns the JobSpawnEach declarations.
- `func (w *Work) Spawns() []*SpawnSpec` -- Spawns returns the static JobSpawn declarations registered on this Work.
- `func (w *Work) StepByID(id string) *WorkStep` -- StepByID returns the step with the given id, or nil if absent.
- `func (w *Work) Steps() []*WorkStep` -- Steps returns the work's steps in insertion order.
- `func (w *Work) TopologicalStepOrder() []string` -- TopologicalStepOrder returns Work item IDs in a stable topological order consistent with their Needs DAG: ties broken by registration order (the order Step / SpawnNode / SpawnNodeForEach was called).

### type WorkDep

WorkDep is the closed type set accepted by Work-layer WorkStep.Needs and the Needs methods on StepGroup, SpawnSpec, and SpawnGenSpec.

```
type WorkDep interface {
    // contains filtered or unexported methods
}
```


### type WorkStep

WorkStep is one unit of work inside a Work.

```
type WorkStep struct {
    // contains filtered or unexported fields
}
```

- `func Step(w *Work, id string, fn any) *WorkStep` -- Step registers a unit of work on this Work.
- `func (s *WorkStep) ContinueOnError() *WorkStep` -- ContinueOnError marks the step's failure as non-blocking for the rest of the Work: in-flight sibling steps are not cancelled, and downstream steps that .Needs() this one still dispatch.
- `func (s *WorkStep) DepIDs() []string` -- DepIDs returns the step IDs this step depends on.
- `func (s *WorkStep) DryRun(fn func(ctx context.Context) error) *WorkStep` -- DryRun installs a dry-run body on this WorkStep.
- `func (s *WorkStep) HasDryRun() bool` -- HasDryRun reports whether a dry-run body has been installed.
- `func (s *WorkStep) ID() string` -- ID returns the step's identifier.
- `func (s *WorkStep) IsContinueOnError() bool` -- IsContinueOnError reports whether this step's failure is non- blocking for sibling cancellation and downstream Needs() dispatch.
- `func (s *WorkStep) IsOptional() bool` -- IsOptional reports whether this step's failure is masked from the Job's rollup outcome.
- `func (s *WorkStep) IsSafeWithoutDryRun() bool` -- IsSafeWithoutDryRun reports whether the step is marked safe.
- `func (s *WorkStep) Needs(deps ...WorkDep) *WorkStep` -- Needs declares hard upstream Step / Spawn dependencies inside the same Work.
- `func (s *WorkStep) Optional() *WorkStep` -- Optional marks the step as non-essential: a failure is recorded (still visible in logs and step status) but does not count toward the Job's rollup outcome.
- `func (s *WorkStep) Output() any` -- Output returns the resolved typed output (after the step completes) or nil.
- `func (s *WorkStep) OutputType() reflect.Type` -- OutputType returns the typed output reflect.Type, or nil for steps that return only error.
- `func (s *WorkStep) Risk(labels ...string) *WorkStep` -- Risk marks a WorkStep as gated by the given operator-acknowledged labels.
- `func (s *WorkStep) Risks() []string` -- Risks returns the labels declared on this step in declaration order with duplicates collapsed.
- `func (s *WorkStep) SafeWithoutDryRun() *WorkStep` -- SafeWithoutDryRun marks a step as having no side effects, so the dispatcher runs the apply Fn directly under --dry-run rather than requiring a separate dry-run body.
- `func (s *WorkStep) SkipIf(fn SkipPredicate) *WorkStep` -- SkipIf registers a predicate the runner evaluates after this step's upstream deps complete.
- `func (s *WorkStep) SkipPredicates() []SkipPredicate` -- SkipPredicates returns the registered skip predicates.

### type Workable

Workable is the interface every dispatchable Job satisfies: a struct that exposes its inner DAG via Work(w).

```
type Workable interface {
    Work(w *Work) (*WorkStep, error)
}
```

- `func CoerceSpawnEachJob(v any) (Workable, error)` -- CoerceSpawnEachJob normalizes the second-return of a JobSpawnEach per-item callback into a Workable.

## Constants

```
const (
    EventStepStart   = "step_start"
    EventStepEnd     = "step_end"
    EventStepSkipped = "step_skipped"
)
```

```
const DefaultCacheTTL = 7 * 24 * time.Hour
```

```
const EventNodeAnnotation = "node_annotation"
```

```
const EventNodeSummary = "node_summary"
```

```
const ExitNotStarted = -1
```

```
const MaxCacheTTL = 35 * 24 * time.Hour
```

```
const Paused = "paused"
```

## Variables

```
var ErrNoProject = errors.New("sparkwing: no .sparkwing/ project found above cwd")
```

```
var ErrSecretMissing = errors.New("sparkwing: secret not found")
```

```
var RuntimePlumbing = struct {
    Keys runtimePlumbingKeys
    Fns  runtimePlumbingFns
}{
    Keys: runtimePlumbingKeys{
        DryRun:            dryRunKey{},
        Runner:            runnerCtxKey{},
        SpawnHandler:      keySpawnHandler,
        StepRange:         stepRangeKey{},
        RefResolver:       keyRefResolver,
        JSONRefResolver:   keyJSONRefResolver,
        PipelineResolver:  keyPipelineResolver,
        PipelineAwaiter:   keyPipelineAwaiter,
        Inputs:            keyInputs,
        PipelineSecrets:   keyPipelineSecrets,
        SecretResolver:    keySecretResolver,
        Logger:            keyLogger,
        Node:              keyNode,
        ResolvedArgs:      keyResolvedArgs,
        ProfileResolution: keyProfileResolution,
    },
    Fns: runtimePlumbingFns{
        PlanInsertChild:        (*Plan).insertChild,
        PlanInsertExpanded:     (*Plan).insertExpanded,
        JobGroupFinalize:       (*JobGroup).finalize,
        WorkStepFn:             func(s *WorkStep) func(ctx context.Context) (any, error) { return s.fn },
        WorkStepMarkDone:       (*WorkStep).markDone,
        SpawnSpecSetResolvedID: (*SpawnSpec).setResolvedID,
        SpawnSpecMarkDone:      (*SpawnSpec).markDone,
    },
}
```

