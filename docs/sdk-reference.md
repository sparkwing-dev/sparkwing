<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference

Every exported symbol in the `sparkwing` package (the SDK you import as `sw`), generated from source. Browse the same thing with cross-links on pkg.go.dev: <https://pkg.go.dev/github.com/sparkwing-dev/sparkwing/sparkwing>. For concepts and usage examples, see [sdk.md](sdk.md).

## Subpackages

Part of the authoring surface too -- a pipeline that builds an image or reads the branch imports these. Each has its own page:

- [`sparkwing/cleanup`](sdk-cleanup.md) -- Package cleanup lets a sparks library register best-effort cleanup for resources outside the step's process tree.
- [`sparkwing/docker`](sdk-docker.md) -- Package docker is the sparkwing SDK's Docker-shelling helper layer: build, push, login, and deterministic tag computation.
- [`sparkwing/git`](sdk-git.md) -- Package git is the sparkwing SDK's repo-inspection helper layer: commit SHA, branch, dirty-tree detection, deterministic fileset hash, tag listing, and safe tag push.
- [`sparkwing/inputs`](sdk-inputs.md) -- Package inputs declares memo inputs for evaluation by the executing host.
- [`sparkwing/services`](sdk-services.md) -- Package services is the sparkwing SDK's sidecar-container helper: start sidecars for a function, wait for readiness, and clean up services whose startup succeeded on return, error, panic, or context cancellation.
- [`sparkwing/sparkwingtest`](sdk-sparkwingtest.md) -- Package sparkwingtest supplies a fake protocol host without a controller or store.

## Functions

- `func Annotate(ctx context.Context, annotation Annotation)` -- Annotate emits a located annotation during execution.
- `func Debug(ctx context.Context, format string, args ...any)` -- Debug emits a debug record; the host decides whether to retain it.
- `func Error(ctx context.Context, format string, args ...any)` -- Error emits an error-level message.
- `func GithubOwnerRepo(slug string) (owner, repo string)` -- GithubOwnerRepo splits a "owner/name" slug into its parts.
- `func Glob(pattern string) ([]string, error)` -- Glob expands a shell-style glob pattern.
- `func Info(ctx context.Context, format string, args ...any)` -- Info emits an info-level message to the active logger.
- `func Inputs[T any](ctx context.Context) T` -- Inputs returns the typed Inputs struct that the orchestrator parsed for the current run -- the same value the pipeline's Plan(ctx, plan, in T, rc) method received.
- `func IsDryRun(ctx context.Context) bool` -- IsDryRun reports whether ctx is in dry-run mode.
- `func Main()` -- Main serves registered pipelines over the node protocol.
- `func Mask(ctx context.Context, value string) error` -- Mask returns only after host registration so subsequent output cannot race masking.
- `func MustSecret(ctx context.Context, name string) string` -- MustSecret is Secret that panics on error.
- `func MustVariable(ctx context.Context, name string) string` -- MustVariable makes a missing required plain value fail the job.
- `func OIDCToken(ctx context.Context, audience string) (string, error)` -- OIDCToken returns an ID token the controller signed for this run, with aud set to audience.
- `func Path(parts ...string) string` -- Path joins parts onto WorkDir() and returns the absolute path.
- `func PipelineSecrets[T any](ctx context.Context) *T` -- PipelineSecrets returns the typed Secrets struct installed on ctx, resolving it when none is installed, or nil when the pipeline doesn't implement SecretsProvider.
- `func ReadFile(path string) ([]byte, error)` -- ReadFile reads the named file.
- `func Register[T any](name string, factory func() Pipeline[T])` -- Register installs a pipeline under the given name.
- `func RegisterEntrypoint[T any](entrypointName string, factory func() Pipeline[T])` -- RegisterEntrypoint declares a named SDK entrypoint.
- `func RunAndAwait[Out, In any](ctx context.Context, pipeline, nodeID string, opts ...AwaitOption) (Out, error)` -- RunAndAwait triggers a fresh run of pipeline and waits for it to reach terminal state, returning the typed output of nodeID from that run.
- `func Secret(ctx context.Context, name string) (string, error)` -- Secret resolves a masked value through the resolver installed on ctx.
- `func StepGet[T any](ctx context.Context, step *WorkStep) T` -- StepGet blocks until step has completed, then returns its typed output as T. Used inside another step's body when composing values from upstream typed steps.
- `func Summary(ctx context.Context, markdown string)` -- Summary emits markdown for the active job or step.
- `func Variable(ctx context.Context, name string) (string, error)` -- Variable distinguishes a declared unmasked value from a secret.
- `func Warn(ctx context.Context, format string, args ...any)` -- Warn emits a warn-level message.
- `func WithCommandEnv(ctx context.Context, env map[string]string) context.Context` -- WithCommandEnv returns a context whose sparkwing.Exec/Bash calls inherit env.
- `func WorkDir() string` -- WorkDir returns the pipeline working directory (the repo root).
- `func WriteFile(path string, data []byte) error` -- WriteFile writes data to the named file with perm 0o644 (creating or truncating).

## Types

### type AdmissionClass

AdmissionClass describes the latency expectations of a pipeline without imposing an absolute queue order.

```
type AdmissionClass string
```

```
const (
    AdmissionCritical    AdmissionClass = "critical"
    AdmissionInteractive AdmissionClass = "interactive"
    AdmissionNormal      AdmissionClass = "normal"
    AdmissionBatch       AdmissionClass = "batch"
)
```


### type AfterRunFn

AfterRunFn receives the final Run error after all retries, or nil on success.

```
type AfterRunFn func(ctx context.Context, err error)
```


### type Annotation

Annotation carries source location and a severity: LevelDebug, LevelInfo, LevelWarn or LevelError.

```
type Annotation struct {
    Level   string
    Message string
    File    string
    Line    int
    Column  int
    EndLine int
}
```


### type Approval

Approval exposes only declarative gate behavior because gates execute no job hooks.

```
type Approval struct {
    // contains filtered or unexported fields
}
```

- `func (a *Approval) ContinueOnError() *Approval` -- ContinueOnError permits dependents after a gate failure.
- `func (a *Approval) ID() string` -- ID permits gate references without exposing executable-job internals.
- `func (a *Approval) Needs(deps ...Dep) *Approval` -- Needs waits for prerequisite jobs before requesting a decision.
- `func (a *Approval) NeedsGenerated(generators ...*Generator) *Approval` -- NeedsGenerated keeps a gate behind the persisted expansion barrier.
- `func (a *Approval) NeedsIfPresent(deps ...Dep) *Approval` -- NeedsIfPresent permits plans whose optional prerequisite is absent.
- `func (a *Approval) OnFailure(id string, body any) *JobNode` -- OnFailure declares and returns a recovery job for a failed gate.
- `func (a *Approval) Optional() *Approval` -- Optional excludes a denied or expired gate from required run success.
- `func (a *Approval) SkipIf(predicate SkipPredicate, opts ...SkipOption) *Approval` -- SkipIf runs in a separate evaluation job before requesting approval, holding no concurrency capacity.

### type ApprovalConfig

ApprovalConfig preserves indefinite waiting when Timeout is zero.

```
type ApprovalConfig struct {
    Message   string
    Timeout   time.Duration
    OnTimeout ApprovalTimeoutPolicy
}
```


### type ApprovalTimeoutPolicy

ApprovalTimeoutPolicy enumerates the resolution applied to an unanswered approval gate when its Timeout elapses.

```
type ApprovalTimeoutPolicy string
```

```
const (
    ApprovalFail    ApprovalTimeoutPolicy = "fail"
    ApprovalDeny    ApprovalTimeoutPolicy = "deny"
    ApprovalApprove ApprovalTimeoutPolicy = "approve"
)
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
- `func WithFreshTimeout(d time.Duration) AwaitOption` -- WithFreshTimeout bounds the total wait, including admission queue time.

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


### type Checkout

Checkout distinguishes absent source settings from explicit zero and false values.

```
type Checkout struct {
    Depth       *int
    FullHistory *bool
    Tags        *bool
    Submodules  *bool
    LFS         *bool
}
```


### type Cmd

Cmd is the chainable command builder returned by Bash and Exec.

```
type Cmd struct {
    // contains filtered or unexported fields
}
```

- `func Bash(ctx context.Context, line string) *Cmd` -- Bash starts building a shell command (run via "bash -c").
- `func Exec(ctx context.Context, name string, args ...string) *Cmd` -- Exec starts building an argv command (no shell).
- `func (c *Cmd) Capture() (ExecResult, error)` -- Capture executes the command silently -- no output log records, just the exec_start banner.
- `func (c *Cmd) Dir(path string) *Cmd` -- Dir sets the working directory for the command.
- `func (c *Cmd) Env(key, value string) *Cmd` -- Env adds (or overrides) a single environment variable.
- `func (c *Cmd) EnvMap(env map[string]string) *Cmd` -- EnvMap merges a map of environment variables into the command.
- `func (c *Cmd) JSON(out any) error` -- JSON runs the command silently and decodes stdout into out via encoding/json.
- `func (c *Cmd) Lines() ([]string, error)` -- Lines runs the command silently and returns stdout split on "\n", with each line trimmed and blanks dropped.
- `func (c *Cmd) MustBeEmpty(reason string) error` -- MustBeEmpty runs the command silently and returns nil only if its stdout (after TrimSpace) is empty.
- `func (c *Cmd) Run() (ExecResult, error)` -- Run executes the command, forwarding stdout/stderr bytes to the logger installed in ctx.
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
- `func (g *ConcurrencyGroup) String() string` -- String includes the group's capacity and effective scope and saturation policy.

### type ConcurrencyLimit

ConcurrencyLimit is the budget a ConcurrencyGroup enforces.

```
type ConcurrencyLimit struct {
    // Capacity is the total budget in author-defined units. With the
    // default member cost of 1 it reads as "max members running at
    // once". Capacity must be positive when the host validates the plan.
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
    // OnLimit is what a member does when the group is full. The zero
    // value is [Queue].
    OnLimit OnLimit
    // QueueTimeout bounds how long a [Queue] member waits for room
    // before failing with failure_reason "queue_timeout". Zero waits
    // indefinitely. Only meaningful with OnLimit [Queue].
    QueueTimeout time.Duration
}
```


### type ConsumeOption

ConsumeOption tunes a JobNode.Consumes declaration.

```
type ConsumeOption func(*consumeEdge)
```

- `func Into(prefix string) ConsumeOption` -- Into stages the consumed producer's artifacts under prefix, with the producer's internal structure preserved.

### type Dep

Dep accepts JobNode, Approval, and JobGroup handles as dependencies.

```
type Dep interface {
    // contains filtered or unexported methods
}
```


### type DirCache

DirCache declares files restored and saved by the host around a job attempt.

```
type DirCache struct {
    // contains filtered or unexported fields
}
```

- `func Dir(path string, key KeySource) DirCache` -- Dir declares a dependency cache rooted at path with explicit key inputs.
- `func GoModules() DirCache` -- GoModules caches downloaded Go modules using go.sum as its input.
- `func Npm() DirCache` -- Npm requests npm's download cache without introducing a package proxy.
- `func (c DirCache) Key(workdir string) (string, error)` -- Key hashes the declaration, platform and first present key file contained in workdir.

### type EnvOption

EnvOption distinguishes process startup settings from body-only environment.

```
type EnvOption func(*envOptions)
```

- `func Startup() EnvOption` -- Startup marks interpreter configuration that must never influence plan declarations.

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
    // Terminated names why a started process was killed rather than
    // exiting on its own: "cancellation" when the run's context ended,
    // "signal" for any other kill. Empty for a normal exit or a process
    // that never started. A signalled process reports ExitCode -1, which
    // collides with ExitNotStarted, so this field -- not ExitCode -- is
    // what tells a kill apart from a failed launch.
    Terminated string
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

```
const (
    // StageAction marks a failure in the node's action: its Run exited
    // non-zero, panicked, or hit its Timeout.
    StageAction FailureStage = iota
    // StageVerify marks a failure in the node's Verify postcondition: the
    // action completed, but the check returned a non-nil error.
    StageVerify
)
```

- `func (s FailureStage) String() string` -- String returns "action" or "verify".

### type GenerateFn

GenerateFn reconstructs declarations from persisted source output without the source job's body environment.

```
type GenerateFn func(context.Context, *Plan) error
```


### type Generator

Generator identifies a source expansion whose settlement precedes dependent jobs.

```
type Generator struct {
    // contains filtered or unexported fields
}
```

- `func (g *Generator) ID() string` -- ID exposes the stable group identifier used in persisted expansion identity.

### type Git

Git is the run-scoped view of a single git working tree, supplied through RunContext.Git.

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
- `func (g *Git) FilesetHash(ctx context.Context) (string, error)` -- FilesetHash returns a deterministic hash of the build context: tracked files and untracked files Git does not ignore, less .dockerignore matches.
- `func (g *Git) IsDirty(ctx context.Context) (bool, error)` -- IsDirty reports whether the working tree has uncommitted changes.
- `func (g *Git) LatestTag(ctx context.Context, prefix string) (string, error)` -- LatestTag returns the highest semver tag with prefix.
- `func (g *Git) Name() string` -- Name returns the bare repo name (the part after the last "/" in Repo).
- `func (g *Git) PushTag(ctx context.Context, tag, message string) error` -- PushTag creates the annotated tag locally and pushes it to origin.
- `func (g *Git) ShortSHA() string` -- ShortSHA returns g.SHA truncated to 12 chars; safe to call on a nil receiver (returns "").
- `func (g *Git) TagsAtHead(ctx context.Context) ([]string, error)` -- TagsAtHead returns every tag pointing at HEAD.
- `func (g *Git) WorkDir() string` -- WorkDir returns the absolute path of the working tree this Git describes, or "" when constructed without a real clone.

### type HelpProvider

HelpProvider is optionally implemented by pipelines to contribute a short description to `sparkwing run <name> --help`.

```
type HelpProvider interface {
    Help() string
}
```


### type InputError

InputError reports a typed decoding failure in pipeline Inputs or a job's WithArgs.

```
type InputError struct {
    Err error
}
```

- `func (e *InputError) Error() string`
- `func (e *InputError) Unwrap() error`

### type JobGroup

JobGroup holds nodes for dependency wiring and dashboard grouping.

```
type JobGroup struct {
    // contains filtered or unexported fields
}
```

- `func GroupJobs(p *Plan, name string, nodes ...*JobNode) *JobGroup` -- GroupJobs groups existing Plan nodes under name and returns a dependency target covering every member.
- `func JobFanOut[T any](p *Plan, name string, items []T, fn func(T) (string, any)) *JobGroup` -- JobFanOut registers one job per item during plan construction.
- `func (g *JobGroup) AfterRun(fn AfterRunFn) *JobGroup` -- AfterRun registers a post-run hook on every member.
- `func (g *JobGroup) Artifacts(globs ...string) *JobGroup` -- Artifacts applies published-file patterns to every group member.
- `func (g *JobGroup) BeforeRun(fn BeforeRunFn) *JobGroup` -- BeforeRun registers a pre-run hook on every member.
- `func (g *JobGroup) Cache(caches ...DirCache) *JobGroup` -- Cache applies dependency-file declarations to every group member.
- `func (g *JobGroup) Concurrency(cg *ConcurrencyGroup, cost ...int) *JobGroup` -- Concurrency enrolls every member of the group in concurrency group g with the given admission cost.
- `func (g *JobGroup) Consumes(producer *JobNode, opts ...ConsumeOption) *JobGroup` -- Consumes stages the given producer's artifacts into every member's workspace before it runs, and implies Needs(producer) on each.
- `func (g *JobGroup) ContinueOnError() *JobGroup` -- ContinueOnError marks every member so downstream dependents proceed even on failure.
- `func (g *JobGroup) Env(key, value string, opts ...EnvOption) *JobGroup` -- Env applies the same startup/body distinction to all members.
- `func (g *JobGroup) Inline() *JobGroup` -- Inline places every member on the dispatcher's host.
- `func (g *JobGroup) Members() []*JobNode` -- Members returns the group's jobs in declaration order.
- `func (g *JobGroup) Memo(key any, opts ...MemoOption) *JobGroup` -- Memo applies one reuse policy to each static or generated group member.
- `func (g *JobGroup) Name() string` -- Name returns the group's declared name, or "" for an unnamed (structural-only) group.
- `func (g *JobGroup) Needs(deps ...Dep) *JobGroup` -- Needs declares an upstream dependency on every member of the group.
- `func (g *JobGroup) NeedsGenerated(generators ...*Generator) *JobGroup` -- NeedsGenerated carries the same expansion barrier to every group member.
- `func (g *JobGroup) NeedsIfPresent(deps ...Dep) *JobGroup` -- NeedsIfPresent applies presence-sensitive ordering to every group member.
- `func (g *JobGroup) NeedsTools(names ...string) *JobGroup` -- NeedsTools restricts every member to agents that have the named tools.
- `func (g *JobGroup) NoProgressTimeout(d time.Duration) *JobGroup` -- NoProgressTimeout sets the per-attempt inactivity timeout on every member.
- `func (g *JobGroup) Optional() *JobGroup` -- Optional marks every member as non-essential.
- `func (g *JobGroup) Prefers(labels ...string) *JobGroup` -- Prefers ranks eligible runners for every member without relaxing requirements, reservations or placement policy.
- `func (g *JobGroup) Requires(labels ...string) *JobGroup` -- Requires constrains runner claims for every non-inline dispatched member.
- `func (g *JobGroup) Retry(attempts int, opts ...RetryOption) *JobGroup` -- Retry configures every member to be re-attempted up to attempts additional times on failure.
- `func (g *JobGroup) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobGroup` -- SkipIf registers a predicate on every member.
- `func (g *JobGroup) Timeout(d time.Duration) *JobGroup` -- Timeout caps the per-attempt duration on every member.
- `func (g *JobGroup) Verify(fn VerifyFn) *JobGroup` -- Verify registers a postcondition check on every member.

### type JobNode

JobNode is a Plan entry wrapping a Workable.

```
type JobNode struct {
    // contains filtered or unexported fields
}
```

- `func Job(p *Plan, id string, x any) *JobNode` -- Job registers a Workable or func(context.Context) error under id.
- `func (n *JobNode) AfterRun(fn AfterRunFn) *JobNode` -- AfterRun registers a hook invoked after Run and all retries finish.
- `func (n *JobNode) Artifacts(globs ...string) *JobNode` -- Artifacts distinguishes published files from the job's typed output.
- `func (n *JobNode) BeforeRun(fn BeforeRunFn) *JobNode` -- BeforeRun registers a hook to run once before the node's Run method on the first attempt.
- `func (n *JobNode) Cache(caches ...DirCache) *JobNode` -- Cache declares files restored through the host-authorized depcache scope.
- `func (n *JobNode) Concurrency(g *ConcurrencyGroup, cost ...int) *JobNode` -- Concurrency enrolls the node in concurrency group g with the given admission cost (default 1).
- `func (n *JobNode) Consumes(producer *JobNode, opts ...ConsumeOption) *JobNode` -- Consumes declares that this node stages the artifacts produced by producer into its workspace before it runs, and implies Needs(producer).
- `func (n *JobNode) ContinueOnError() *JobNode` -- ContinueOnError tells the orchestrator that downstream dependents should proceed even when this node fails.
- `func (n *JobNode) Env(key, value string, opts ...EnvOption) *JobNode` -- Env defaults to body scope; startup declarations take effect before attempt process creation.
- `func (n *JobNode) Generate(fn GenerateFn) *Generator` -- Generate declares an expansion after source success or memo reuse; the host owns acceptance.
- `func (n *JobNode) ID() string`
- `func (n *JobNode) Inline() *JobNode` -- Inline places the node on the dispatcher's host.
- `func (n *JobNode) Memo(key any, opts ...MemoOption) *JobNode` -- Memo declares result reuse from a MemoInput recipe or a MemoKeyFn callback.
- `func (n *JobNode) Needs(deps ...Dep) *JobNode` -- Needs declares hard upstream dependencies.
- `func (n *JobNode) NeedsGenerated(generators ...*Generator) *JobNode` -- NeedsGenerated waits for expansion settlement and all generated prerequisites.
- `func (n *JobNode) NeedsIfPresent(deps ...Dep) *JobNode` -- NeedsIfPresent ignores absent dependencies but preserves failure ordering for present jobs.
- `func (n *JobNode) NeedsTools(names ...string) *JobNode` -- NeedsTools restricts the job to agents that have every named tool on their PATH.
- `func (n *JobNode) NoProgressTimeout(d time.Duration) *JobNode` -- NoProgressTimeout caps how long an attempt may run without emitting a node log record.
- `func (n *JobNode) OnFailure(id string, x any) *JobNode` -- OnFailure declares and returns a recovery job that runs when this job fails.
- `func (n *JobNode) Optional() *JobNode` -- Optional marks the node as non-essential: a failure is logged as a warning and does not count toward the run's overall success/fail outcome.
- `func (n *JobNode) Prefers(labels ...string) *JobNode` -- Prefers ranks eligible runners without relaxing requirements, reservations or placement policy.
- `func (n *JobNode) Requires(labels ...string) *JobNode` -- Requires records label terms used to filter runner claims for non-inline dispatched jobs.
- `func (n *JobNode) Retry(attempts int, opts ...RetryOption) *JobNode` -- Retry configures the node to be re-attempted up to attempts additional times on failure.
- `func (n *JobNode) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobNode` -- SkipIf registers a predicate evaluated on the executing host after this node's dependencies complete and its concurrency groups are acquired.
- `func (n *JobNode) StartsChildren(pipelines ...string) *JobNode` -- StartsChildren appends pipelines in the parent repository that this job may start with RunAndAwait.
- `func (n *JobNode) StartsChildrenIn(repository string, pipelines ...string) *JobNode` -- StartsChildrenIn declares child pipelines in a canonical repository key such as "gh:123" or "git:host/org/repo".
- `func (n *JobNode) Timeout(d time.Duration) *JobNode` -- Timeout caps the per-attempt duration.
- `func (n *JobNode) Verify(fn VerifyFn) *JobNode` -- Verify checks a postcondition after each successful action attempt.

### type KeySource

KeySource names files whose content determines a dependency cache key.

```
type KeySource struct {
    // contains filtered or unexported fields
}
```

- `func KeyFromFile(path string) KeySource` -- KeyFromFile declares a lockfile relative to the job's workspace.

### type LogRecord

LogRecord is the structured unit every Logger receives.

```
type LogRecord struct {
    TS    time.Time      `json:"ts"`
    Level string         `json:"level,omitempty"` // "info" | "warn" | "error"
    JobID string         `json:"job,omitempty"`
    Step  string         `json:"step,omitempty"`  // active step ID
    Event string         `json:"event,omitempty"` // "" for a plain message; otherwise one of the Event* constants
    Msg   string         `json:"msg,omitempty"`
    Attrs map[string]any `json:"attrs,omitempty"`
    // contains filtered or unexported fields
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


### type MemoInput

MemoInput declares built-in inputs for evaluation by the executing host.

```
type MemoInput = nodeproto.MemoInput
```


### type MemoKeyFn

MemoKeyFn returns a JSON value or NoMemo.

```
type MemoKeyFn func(context.Context) (MemoValue, error)
```


### type MemoOption

MemoOption keeps unset TTL distinct from an explicit duration.

```
type MemoOption func(*memoOptions)
```

- `func MemoTTL(d time.Duration) MemoOption` -- MemoTTL declares retention subject to the host's ceiling at plan acceptance.

### type MemoValue

MemoValue is an author value the executing host hashes as canonical JSON.

```
type MemoValue = any
```

- `func MemoKey(parts ...any) MemoValue` -- MemoKey collects ordered literal parts for the host to hash.

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

```
const (
    // Queue waits for room, then runs. Waiters that fit run oldest-first; a
    // waiter that cannot fit in the currently available weighted budget does
    // not block later waiters that do fit unless younger backfilled holders
    // are what keep the older waiter from fitting.
    Queue OnLimit = "queue"
    // Fail errors the node immediately.
    Fail OnLimit = "fail"
    // Skip resolves the node as a no-op without running it.
    Skip OnLimit = "skip"
    // CancelOthers is best-effort preemption ("newest wins"): it evicts
    // running members oldest-first until this node fits, then takes the
    // slot and runs immediately. Eviction is cooperative -- an evicted
    // member is signaled to stop and winds down on its own -- so this node
    // may briefly run alongside a still-draining victim, and side effects a
    // member completed before the cancel signal are not rolled back. Use
    // [Queue] when you need strict mutual exclusion with no overlap.
    CancelOthers OnLimit = "cancel_others"
)
```


### type Outcome

Outcome is a persisted terminal job verdict.

```
type Outcome string
```

```
const (
    Success    Outcome = "success"
    Failed     Outcome = "failed"
    Cancelled  Outcome = "cancelled"
    Skipped    Outcome = "skipped"
    Superseded Outcome = "superseded"
)
```

```
const Memoized Outcome = "memoized"
```

- `func (o Outcome) OK() bool` -- OK reports whether the verdict satisfies a successful dependency.
- `func (o Outcome) Terminal() bool` -- Terminal reports whether the verdict belongs to the terminal vocabulary.

### type ParallelFailurePolicy

ParallelFailurePolicy controls whether independent Work items finish after one of their siblings fails.

```
type ParallelFailurePolicy string
```

```
const (
    // FailFast cancels ordinary in-flight siblings after the first decisive
    // failure. It is the default and preserves the historical Work behavior.
    FailFast ParallelFailurePolicy = "fail_fast"
    // CollectAll lets every independent or already-ready item finish so one run
    // can report the full failure set. It does not satisfy a failed prerequisite;
    // downstream Needs still require success or an explicit ContinueOnError.
    CollectAll ParallelFailurePolicy = "collect_all"
)
```


### type Pipeline

Pipeline is the canonical pipeline shape: every pipeline declares a typed Inputs struct and populates a Plan that the SDK constructs and passes in.

```
type Pipeline[T any] interface {
    Plan(ctx context.Context, plan *Plan, in T, rc RunContext) error
}
```


### type Plan

Plan holds the job graph populated by a pipeline's Plan method.

```
type Plan struct {
    // contains filtered or unexported fields
}
```

- `func NewPlan() *Plan`
- `func (p *Plan) AdmissionClass(class AdmissionClass) *Plan` -- AdmissionClass sets the pipeline's local contention class.
- `func (p *Plan) Approval(id string, cfg ApprovalConfig) *Approval` -- Approval declares a manual gate whose owner enforces waiting and timeout policy.
- `func (p *Plan) Checkout(config Checkout) *Plan` -- Checkout declares source preferences for host resolution without reading YAML in the SDK.
- `func (p *Plan) ClaimWait(d time.Duration) *Plan` -- ClaimWait declares how long each ready job may wait for a runner claim.
- `func (p *Plan) Concurrency(g *ConcurrencyGroup, cost ...int) *Plan` -- Concurrency gates the whole run on concurrency group g: the run acquires each declared plan-level budget before any node dispatches and releases it when the run reaches a terminal status.
- `func (p *Plan) Priority(n int) *Plan` -- Priority sets this run's local admission priority.

### type Produces

Produces is a zero-size marker embedded in a Job to declare its output contract at the type level.

```
type Produces[T any] struct{}
```


### type PullRequest

PullRequest carries the GitHub pull_request fields a PR gate reads.

```
type PullRequest struct {
    Number int
    // Action is the pull_request event action that fired the run
    // (opened, synchronize, reopened).
    Action  string
    BaseRef string
    // BaseSHA is the tip commit of the base branch at event time.
    BaseSHA string
    HeadRef string
    // HeadSHA is the PR head commit the run checks out.
    HeadSHA string
}
```


### type Ref

Ref is a typed reference to another node's output.

```
type Ref[T any] struct {
    // JobID identifies the producing job. For in-run refs this is
    // a sibling node id; for cross-pipeline refs this is a node id
    // inside Pipeline.
    JobID string

    // Pipeline names the upstream pipeline when the ref is
    // cross-pipeline. Empty means in-run.
    Pipeline string

    // MaxAge bounds the freshness of a cross-pipeline run lookup.
    // Zero means "any eligible successful run, however old." Ignored for
    // in-run refs.
    MaxAge time.Duration

    // AnyRun permits cross-pipeline output from runs without verified default-branch provenance.
    AnyRun bool
}
```

- `func RefTo[T any](n *JobNode) Ref[T]` -- RefTo returns a Ref[T] pointing at an in-run node's output.
- `func RefToLastRun[T any](pipeline, nodeID string, opts ...RefOption) Ref[T]` -- RefToLastRun returns a Ref[T] pointing at node nodeID in the most recent successful verified default-branch run of pipeline.
- `func (r Ref[T]) Get(ctx context.Context) T` -- Get resolves the reference to a typed T value.
- `func (r Ref[T]) Job() string` -- Job returns the upstream node id this reference points at.
- `func (r Ref[T]) TryGet(ctx context.Context) (T, bool)` -- TryGet resolves the reference like Get but reports an absent upstream output instead of panicking.

### type RefOption

RefOption tunes a cross-pipeline ref constructor (RefToLastRun).

```
type RefOption func(*refOpts)
```

- `func FromAnyRun() RefOption` -- FromAnyRun permits a cross-pipeline reference to read successful runs from any branch or intake, including uploads.
- `func MaxAge(d time.Duration) RefOption` -- MaxAge bounds cross-pipeline ref resolution to runs whose finished_at is within d.

### type RetryConfig

RetryConfig is the retry envelope for a Job.

```
type RetryConfig struct {
    Attempts int
    Backoff  time.Duration
    // contains filtered or unexported fields
}
```


### type RetryOption

RetryOption tunes a Retry(...) call.

```
type RetryOption func(*RetryConfig)
```

- `func Backoff(d time.Duration) RetryOption` -- Backoff declares retry delay without selecting an execution placement.

### type RunContext

RunContext is the typed environment every Plan and Job sees, populated by the orchestrator at dispatch time.

```
type RunContext struct {
    RunID string

    // Pipeline is the invoked project name, including any alias.
    Pipeline string

    // Git is the run's view of the cloned working tree.
    // Live methods (IsDirty, FilesetHash, …) shell
    // out fresh each call; data fields (SHA, Branch, Repo, RepoURL)
    // are the trigger-time snapshot.
    Git *Git

    Trigger TriggerInfo

    StartedAt time.Time

    // NoMemo bypasses memo reads while retaining writes.
    NoMemo bool

    // DryRun carries the same sealed mode as IsDryRun during planning and execution.
    DryRun bool
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

### type Scope

Scope selects how far a ConcurrencyGroup's budget reaches: only the nodes of one run, every run on one machine, or the whole fleet coordinating through a shared backend.

```
type Scope string
```

```
const (
    // ScopeRun bounds the budget to the nodes of a single run.
    ScopeRun Scope = "run"
    // ScopeBox bounds a job-level budget to one machine, even when a controller
    // dispatches several runs to it. Pipeline acceptance refuses box-scoped
    // plan groups because a run can span machines. Use [JobNode.Concurrency].
    ScopeBox Scope = "box"
    // ScopeGlobal pools the budget across runs naming the group at the same
    // plan or job level, coordinated through the shared backend. The zero value.
    ScopeGlobal Scope = "global"
)
```


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

- `func SkipBudget(d time.Duration) SkipOption` -- SkipBudget sets the timeout applied to each predicate on the node.

### type SkipPredicate

SkipPredicate is a function evaluated after upstream dependencies complete.

```
type SkipPredicate func(ctx context.Context) bool
```


### type StepError

StepError attributes a failure to a step while preserving its underlying cause.

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

### type TriggerInfo

TriggerInfo describes the trigger that started the run.

```
type TriggerInfo struct {
    Source string // "manual", "push", "schedule", "webhook"
    User   string // invoker identity, when known

    // PullRequest is non-nil when the run was started by a GitHub
    // pull_request event. RunContext.Git already carries the PR head
    // (SHA, Branch); this adds the base ref and PR number a gate needs
    // to diff against the target branch or post back to the PR.
    PullRequest *PullRequest
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


### type WithArgs

WithArgs is the embedded helper a job uses to declare typed args.

```
type WithArgs[T any] struct {
    // contains filtered or unexported fields
}
```

- `func (w *WithArgs[T]) Args(ctx context.Context) T` -- Args returns the resolved typed args for the current run.

### type Work

Work is the inner DAG of a Job.

```
type Work struct {
    // contains filtered or unexported fields
}
```

- `func NewWork() *Work` -- NewWork returns an empty Work.
- `func (w *Work) FailurePolicy(policy ParallelFailurePolicy) *Work` -- FailurePolicy chooses fail_fast or collect_all without changing dependency requirements.

### type WorkDep

WorkDep is the closed type set accepted by Work-layer WorkStep.Needs and StepGroup.Needs.

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
- `func (s *WorkStep) DryRun(fn func(ctx context.Context) error) *WorkStep` -- DryRun installs a dry-run body on this WorkStep.
- `func (s *WorkStep) Finally() *WorkStep` -- Finally marks a step as cleanup.
- `func (s *WorkStep) ID() string` -- ID returns the step's identifier.
- `func (s *WorkStep) Needs(deps ...WorkDep) *WorkStep` -- Needs declares hard upstream Step dependencies inside the same Work.
- `func (s *WorkStep) Optional() *WorkStep` -- Optional marks the step as non-essential: a failure is recorded (still visible in logs and step status) but does not count toward the Job's rollup outcome.
- `func (s *WorkStep) Risk(labels ...string) *WorkStep` -- Risk marks a WorkStep as gated by the given operator-acknowledged labels.
- `func (s *WorkStep) SafeWithoutDryRun() *WorkStep` -- SafeWithoutDryRun marks a step as having no side effects, so the dispatcher runs the apply Fn directly under --dry-run rather than requiring a separate dry-run body.
- `func (s *WorkStep) SkipIf(fn SkipPredicate) *WorkStep` -- SkipIf registers a predicate the runner evaluates after this step's upstream deps complete.

### type Workable

Workable is the interface every dispatchable Job satisfies: a struct that exposes its inner DAG via Work(w).

```
type Workable interface {
    Work(w *Work) (*WorkStep, error)
}
```


## Constants

```
const (
    LevelDebug = "debug"
    LevelInfo  = "info"
    LevelWarn  = "warn"
    LevelError = "error"
)
```

```
const ExitNotStarted = -1
```

```
const NoMemo memoBypass = true
```

## Variables

```
var ErrOIDCUnavailable = errors.New("sparkwing: no controller issues OIDC tokens for this run")
```

```
var ErrRefAbsent = errors.New("sparkwing: referenced output is absent")
```

```
var ErrSecretMissing = secrets.ErrSecretMissing
```
