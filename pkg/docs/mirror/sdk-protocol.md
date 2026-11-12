<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/protocol`

Package nodeproto defines protocol v2 requests, frames and strict declarations.

Import as `swnodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func JSONFieldType(kind reflect.Type, key string) (reflect.Type, bool)` -- JSONFieldType finds the Go type for an exact JSON wire field name.
- `func RequestFieldNames() []string` -- RequestFieldNames lists every operation-specific request field by JSON name.

## Types

### type Annotation

Annotation is one annotation as the host accepts it, the controller stores it and the run views return it.

```
type Annotation struct {
    Level   string `json:"level"`
    Message string `json:"message"`
    File    string `json:"file,omitempty"`
    Line    int    `json:"line,omitempty"`
    EndLine int    `json:"end_line,omitempty"`
    Column  int    `json:"column,omitempty"`
}
```


### type AnnotationRequest

AnnotationRequest preserves source location and optional step attribution.

```
type AnnotationRequest struct {
    Annotation
    Step string `json:"step,omitempty"`
}
```


### type ApprovalSpec

ApprovalSpec keeps zero timeout indefinite; the owner's deadline remains separate.

```
type ApprovalSpec struct {
    Message   string `json:"message,omitempty"`
    TimeoutMS *int64 `json:"timeout_ms,omitempty"`
    OnTimeout string `json:"on_timeout,omitempty"`
}
```


### type ArgumentDescription

ArgumentDescription omits language-specific field names from the shared wire schema.

```
type ArgumentDescription struct {
    Name        string   `json:"name"`
    Short       string   `json:"short,omitempty"`
    Type        string   `json:"type"`
    Required    bool     `json:"required"`
    Description string   `json:"desc,omitempty"`
    Default     *string  `json:"default,omitempty"`
    Enum        []string `json:"enum,omitempty"`
    Secret      bool     `json:"secret,omitempty"`
    JobID       string   `json:"job_id,omitempty"`
}
```


### type ArtifactInput

ArtifactInput declares the dependency edge used to authorize artifact reads.

```
type ArtifactInput struct {
    Producer string `json:"producer"`
    Into     string `json:"into,omitempty"`
}
```


### type CacheSpec

CacheSpec is wire-only so SDK binaries need not link engine storage implementations.

```
type CacheSpec struct {
    Name     string   `json:"name"`
    Path     string   `json:"path,omitempty"`
    Resolver string   `json:"resolver,omitempty"`
    KeyScope string   `json:"key_scope,omitempty"`
    KeyFiles []string `json:"key_files"`
}
```


### type ChildPipeline

ChildPipeline names an allowed child pipeline; an empty repository inherits the parent repository.

```
type ChildPipeline struct {
    Pipeline   string `json:"pipeline"`
    Repository string `json:"repository,omitempty"`
}
```


### type ClosureSpec

ClosureSpec references executable code; persisted generated specs never serialize closures.

```
type ClosureSpec struct {
    SkipIf    []string `json:"skip_if,omitempty"`
    BeforeRun []string `json:"before_run,omitempty"`
    Verify    string   `json:"verify,omitempty"`
    AfterRun  []string `json:"after_run,omitempty"`
    MemoKey   string   `json:"memo_key,omitempty"`
    Generate  []string `json:"generate,omitempty"`
}
```


### type ConcurrencySpec

ConcurrencySpec declares membership at plan or job scope; the store derives scoped keys.

```
type ConcurrencySpec struct {
    Group          string  `json:"group"`
    Scope          *string `json:"scope,omitempty"`
    Capacity       *int    `json:"capacity,omitempty"`
    Cost           *int    `json:"cost,omitempty"`
    OnLimit        *string `json:"on_limit,omitempty"`
    QueueTimeoutMS *int64  `json:"queue_timeout_ms,omitempty"`
}
```


### type DescribeDoc

DescribeDoc is the sole source of secret declarations; plan JSON may never repeat them.

```
type DescribeDoc struct {
    Pipelines []PipelineDescription `json:"pipelines"`
}
```

- `func (d DescribeDoc) Pipeline(entrypoint string) (PipelineDescription, bool)` -- Pipeline selects SDK metadata by its configured entrypoint.

### type EngineIdentity

EngineIdentity permits actionable mismatch diagnostics without stamping every request.

```
type EngineIdentity struct {
    Version string `json:"version"`
}
```


### type EnvValue

EnvValue distinguishes interpreter startup from SDK-applied body environment.

```
type EnvValue struct {
    Value   string `json:"value"`
    Startup bool   `json:"startup,omitempty"`
}
```


### type EnvironmentDescription

EnvironmentDescription documents user inputs rather than transport or credential variables.

```
type EnvironmentDescription struct {
    Name        string  `json:"name"`
    Description string  `json:"description,omitempty"`
    Default     *string `json:"default,omitempty"`
}
```


### type EvalResult

EvalResult can carry scalar closure values or generated jobs without changing framing.

```
type EvalResult struct {
    Arguments         []ArgumentDescription `json:"arguments,omitempty"`
    DeclarationsKnown bool                  `json:"declarations_known,omitempty"`
    Value             json.RawMessage       `json:"value"`
    Error             *apierr.Error         `json:"error,omitempty"`
}
```


### type ExecCancel

ExecCancel is an SDK frame asking the host to kill a running command.

```
type ExecCancel struct {
    ID string `json:"exec_cancel"`
}
```


### type ExecDone

ExecDone is the host's last line for a command.

```
type ExecDone struct {
    ID         string `json:"exec_done"`
    ExitCode   int    `json:"exit_code"`
    Signaled   bool   `json:"signaled,omitempty"`
    StartError string `json:"start_error,omitempty"`
    NotFound   bool   `json:"not_found,omitempty"`
    Stdout     []byte `json:"stdout,omitempty"`
    Stderr     []byte `json:"stderr,omitempty"`
}
```


### type ExecOutput

ExecOutput is a host line on the request pipe carrying captured bytes in order.

```
type ExecOutput struct {
    ID     string `json:"exec_output"`
    Stream string `json:"stream"`
    Data   []byte `json:"data"`
}
```


### type ExecRequest

ExecRequest is an SDK frame asking the host to run one command.

```
type ExecRequest struct {
    ID      string   `json:"exec"`
    Step    string   `json:"step,omitempty"`
    Argv    []string `json:"argv"`
    Dir     string   `json:"dir"`
    Env     []string `json:"env"`
    Capture bool     `json:"capture,omitempty"`
    Stdin   []byte   `json:"stdin,omitempty"`
}
```


### type Frame

Frame represents one reply, log record or host exec request on the isolated frame pipe, never stdout.

```
type Frame struct {
    Reply      *Reply
    Log        *FrameLog
    Exec       *ExecRequest
    ExecCancel *ExecCancel
}
```

- `func ParseFrame(line []byte) (Frame, error)` -- ParseFrame rejects ambiguous frames and uses apierr.DecodeInner for reply errors; unknown log events remain readable.

### type FrameLog

FrameLog has no job field because the host, not pipeline code, assigns log attribution.

```
type FrameLog struct {
    Stream     string          `json:"stream,omitempty"`
    StreamData string          `json:"stream_data,omitempty"`
    StreamEnd  bool            `json:"stream_end,omitempty"`
    TS         string          `json:"ts"`
    Level      string          `json:"level,omitempty"`
    Step       string          `json:"step,omitempty"`
    Event      string          `json:"event,omitempty"`
    Message    string          `json:"msg,omitempty"`
    Attrs      json.RawMessage `json:"attrs,omitempty"`
}
```


### type Git

Git is the trigger-time snapshot of the run's checkout.

```
type Git struct {
    SHA           string `json:"sha"`
    Branch        string `json:"branch"`
    DefaultBranch string `json:"default_branch,omitempty"`
    Repo          string `json:"repo,omitempty"`
    RepoURL       string `json:"repo_url,omitempty"`
}
```


### type HelloResult

HelloResult identifies the current SDK protocol.

```
type HelloResult struct {
    Protocol int         `json:"protocol"`
    SDK      SDKIdentity `json:"sdk"`
}
```


### type HelperRequest

HelperRequest invokes host behavior with the pipeline process's environment.

```
type HelperRequest struct {
    Step      string          `json:"step,omitempty"`
    Operation string          `json:"operation"`
    Dir       string          `json:"dir"`
    Env       []string        `json:"env"`
    Args      json.RawMessage `json:"args"`
}
```


### type HelperResponse

HelperResponse carries the typed result or a classified host failure.

```
type HelperResponse struct {
    Result json.RawMessage `json:"result"`
    Error  string          `json:"error,omitempty"`
    Code   string          `json:"code,omitempty"`
}
```


### type HostLine

HostLine is a host-initiated line on the request pipe; exactly one field is set.

```
type HostLine struct {
    Output *ExecOutput
    Done   *ExecDone
}
```

- `func ParseHostLine(line []byte) (HostLine, bool, error)` -- ParseHostLine reports whether line is a host-initiated line rather than a request.

### type JobFailure

JobFailure gives recovery code the source failure without inventing a second error envelope.

```
type JobFailure struct {
    Job   string        `json:"job"`
    Stage string        `json:"stage"`
    Error *apierr.Error `json:"error"`
}
```


### type JobSpec

JobSpec is flat so every placement validates the same execution envelope.

```
type JobSpec struct {
    Arguments      []ArgumentDescription `json:"arguments,omitempty"`
    ID             string                `json:"id"`
    Needs          []string              `json:"deps,omitempty"`
    NeedsIfPresent []string              `json:"deps_if_present,omitempty"`
    NeedsGenerated []string              `json:"needs_generated,omitempty"`
    Groups         []string              `json:"groups,omitempty"`
    Env            map[string]EnvValue   `json:"env,omitempty"`
    Artifacts      []string              `json:"artifacts,omitempty"`
    Consumes       []ArtifactInput       `json:"consumes,omitempty"`
    References     []PipelineReference   `json:"pipeline_refs,omitempty"`
    // Children names the pipelines this job may start as child runs; intake refuses any other.
    Children            []ChildPipeline `json:"children,omitempty"`
    Approval            *ApprovalSpec   `json:"approval,omitempty"`
    OnFailureOf         string          `json:"on_failure_of,omitempty"`
    Retry               *RetrySpec      `json:"retry,omitempty"`
    TimeoutMS           *int64          `json:"timeout_ms,omitempty"`
    NoProgressTimeoutMS *int64          `json:"no_progress_timeout_ms,omitempty"`
    // Cores is the CPU the job asks local admission to reserve; absent means one core.
    Cores           *float64          `json:"cores,omitempty"`
    Requires        []string          `json:"requires,omitempty"`
    Prefers         []string          `json:"prefers,omitempty"`
    Inline          bool              `json:"inline,omitempty"`
    Optional        bool              `json:"optional,omitempty"`
    ContinueOnError bool              `json:"continue_on_error,omitempty"`
    Memo            *MemoSpec         `json:"memo,omitempty"`
    Caches          []CacheSpec       `json:"caches,omitempty"`
    Concurrency     []ConcurrencySpec `json:"concurrency,omitempty"`
    Closures        ClosureSpec       `json:"closures,omitzero"`
    SkipIfBudgetMS  *int64            `json:"skip_if_budget_ms,omitempty"`
    Steps           []StepSpec        `json:"steps,omitempty"`
    ResultStep      string            `json:"result_step,omitempty"`
    FailurePolicy   string            `json:"failure_policy,omitempty"`
}
```


### type MaskRequest

MaskRequest registers a value with every output sink for the bound process.

```
type MaskRequest struct {
    Value string `json:"value"`
}
```


### type MemoInput

MemoInput declares inputs evaluated by the executing host against its checkout and job environment.

```
type MemoInput struct {
    Kind     string      `json:"kind"`
    Value    string      `json:"value,omitempty"`
    Patterns []string    `json:"patterns,omitempty"`
    Root     string      `json:"root,omitempty"`
    Names    []string    `json:"names,omitempty"`
    Parts    []any       `json:"parts,omitempty"`
    Inputs   []MemoInput `json:"inputs,omitempty"`
}
```

- `func (input *MemoInput) UnmarshalJSON(raw []byte) error` -- UnmarshalJSON preserves literal numbers through plan persistence and replay.

### type MemoSpec

MemoSpec enables result reuse independently of dependency-directory caches.

```
type MemoSpec struct {
    TTLMS *int64     `json:"ttl_ms,omitempty"`
    Input *MemoInput `json:"input,omitempty"`
}
```


### type Op

Op keeps the complete launch operation vocabulary separate from requests.

```
type Op string
```

```
const (
    OpHello    Op  = "hello"
    OpDescribe Op  = "describe"
    OpPlan     Op  = "plan"
    OpRunStep  Op  = "run_step"
    OpEval     Op  = "eval"
    OpCancel   Op  = "cancel"
)
```


### type OperationFields

OperationFields names the request fields an operation must carry and may carry, by JSON name.

```
type OperationFields struct{ Required, Allowed []string }
```

- `func RequestFields(op Op) OperationFields` -- RequestFields returns the field rule Validate enforces for op, which the published request schema renders.

### type PipelineDescription

PipelineDescription supports both SDKs without linking engine registration or YAML code.

```
type PipelineDescription struct {
    Name        string                   `json:"name"`
    Error       *apierr.Error            `json:"error,omitempty"`
    Short       string                   `json:"short,omitempty"`
    Help        string                   `json:"help,omitempty"`
    Examples    []UsageExample           `json:"examples,omitempty"`
    Args        []ArgumentDescription    `json:"args"`
    EnvVars     []EnvironmentDescription `json:"env_vars,omitempty"`
    Extra       bool                     `json:"extra,omitempty"`
    Risks       []string                 `json:"risks,omitempty"`
    RisksByStep []StepRisks              `json:"risks_by_step,omitempty"`
    Secrets     []SecretDeclaration      `json:"secrets,omitempty"`
}
```


### type PipelineReference

PipelineReference declares a cross-run read without granting undeclared blob access.

```
type PipelineReference struct {
    Pipeline   string `json:"pipeline"`
    Job        string `json:"job"`
    FromAnyRun bool   `json:"from_any_run,omitempty"`
    // FromProvenRun reads only a run at the reader's sealed commit, proven for
    // the reader's repository, that selected every job without dry run.
    FromProvenRun bool `json:"from_proven_run,omitempty"`
}
```


### type PlanDoc

PlanDoc excludes host-owned state so SDK declarations cannot rewrite sealed authority.

```
type PlanDoc struct {
    Jobs           []JobSpec         `json:"jobs"`
    Concurrency    []ConcurrencySpec `json:"concurrency,omitempty"`
    Priority       *int              `json:"priority,omitempty"`
    ClaimWaitMS    *int64            `json:"claim_wait_ms,omitempty"`
    AdmissionClass *string           `json:"admission_class,omitempty"`
    Source         *SourceSpec       `json:"source,omitempty"`
}
```


### type ProcessBinding

ProcessBinding is absent during describe and plan, where no job routes may be used.

```
type ProcessBinding struct {
    RunID  string        `json:"run_id"`
    JobID  string        `json:"job_id"`
    Routes ProcessRoutes `json:"routes"`
}
```


### type ProcessRoutes

ProcessRoutes holds only the delegated node-host bearer, never a controller credential.

```
type ProcessRoutes struct {
    URL    string `json:"url"`
    Bearer string `json:"bearer"`
}
```


### type PullRequest

PullRequest is the pull request a webhook run was triggered for.

```
type PullRequest struct {
    Number  int    `json:"number"`
    Action  string `json:"action,omitempty"`
    BaseRef string `json:"base_ref,omitempty"`
    BaseSHA string `json:"base_sha,omitempty"`
    HeadRef string `json:"head_ref,omitempty"`
    HeadSHA string `json:"head_sha,omitempty"`
}
```


### type Reader

Reader splits a pipe into lines.

```
type Reader struct {
    // contains filtered or unexported fields
}
```

- `func NewReader(r io.Reader) *Reader` -- NewReader reads lines from r.
- `func (r *Reader) Next() (Frame, error)` -- Next reads and parses the next frame-pipe line with ParseFrame.
- `func (r *Reader) NextRequest() (Request, error)` -- NextRequest reads and parses the next request-pipe line with ParseRequest.
- `func (r *Reader) ReadLine() ([]byte, error)` -- ReadLine returns the next line without its newline or a trailing carriage return.

### type Reply

Reply carries exactly one result or canonical error, decoded by apierr.DecodeInner.

```
type Reply struct {
    ID     string          `json:"reply"`
    OK     bool            `json:"ok"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *apierr.Error   `json:"error,omitempty"`
}
```

- `func (r Reply) Decode(result any) error` -- Decode validates reply, RunResult and EvalResult errors with apierr.DecodeInner before returning typed results.

### type Request

Request binds execution by job and step while keeping credentials only in hello memory.

```
type Request struct {
    ID       string            `json:"id"`
    Op       Op                `json:"op"`
    Protocol int               `json:"protocol,omitempty"`
    Engine   *EngineIdentity   `json:"engine,omitempty"`
    Runner   *RunnerIdentity   `json:"runner,omitempty"`
    Binding  *ProcessBinding   `json:"binding,omitempty"`
    Pipeline string            `json:"pipeline,omitempty"`
    Args     map[string]string `json:"args,omitzero"`
    Run      *RunContext       `json:"run,omitempty"`
    Job      string            `json:"job,omitempty"`
    Step     string            `json:"step,omitempty"`
    Attempt  int               `json:"attempt,omitempty"`
    Settled  []string          `json:"settled,omitzero"`
    Failure  *JobFailure       `json:"failure,omitempty"`
    Closure  string            `json:"closure,omitempty"`
    Input    json.RawMessage   `json:"input,omitempty"`
    // Target identifies the active request cancelled by OpCancel, including eval without job/step.
    Target string `json:"target,omitempty"`
}
```

- `func ParseRequest(line []byte) (Request, error)` -- ParseRequest ignores additive fields, checks operation fields and decodes failure.error with apierr.DecodeInner.
- `func (r Request) Validate() error` -- Validate refuses fields that contradict the selected operation before invoking pipeline code.

### type RetrySpec

RetrySpec counts application retries only; disruptions do not consume this budget.

```
type RetrySpec struct {
    Attempts  int    `json:"attempts"`
    BackoffMS *int64 `json:"backoff_ms,omitempty"`
}
```


### type RunContext

RunContext is a sealed host snapshot; SDKs never query the controller for these facts.

```
type RunContext struct {
    RunID     string    `json:"run_id"`
    Pipeline  string    `json:"pipeline"`
    Git       *Git      `json:"git,omitempty"`
    Trigger   *Trigger  `json:"trigger,omitempty"`
    StartedAt time.Time `json:"started_at,omitzero"`
    NoMemo    bool      `json:"no_memo,omitempty"`
    DryRun    bool      `json:"dry_run,omitempty"`
}
```


### type RunResult

RunResult separates body failure from an SDK failure to execute the request.

```
type RunResult struct {
    Outcome string          `json:"outcome"`
    Error   *apierr.Error   `json:"error,omitempty"`
    Output  json.RawMessage `json:"output,omitempty"`
}
```


### type RunnerIdentity

RunnerIdentity comes from the granted claim, never self-reported SDK environment.

```
type RunnerIdentity struct {
    Name   string   `json:"name"`
    Type   string   `json:"type"`
    Labels []string `json:"labels"`
}
```


### type SDKIdentity

SDKIdentity lets a mismatch name the package that must be rebuilt or upgraded.

```
type SDKIdentity struct {
    Language string `json:"language"`
    Version  string `json:"version"`
}
```


### type SecretDeclaration

SecretDeclaration names required authority without carrying a secret value.

```
type SecretDeclaration struct {
    Name     string `json:"name"`
    Optional bool   `json:"optional,omitempty"`
}
```


### type ServiceAcknowledged

ServiceAcknowledged encodes {} after a bound node-host write has succeeded.

```
type ServiceAcknowledged struct{}
```


### type SourceSpec

SourceSpec expresses checkout policy without granting repository authority.

```
type SourceSpec struct {
    FullHistory *bool `json:"full_history,omitempty"`
    Depth       *int  `json:"depth,omitempty"`
    Tags        *bool `json:"tags,omitempty"`
    Submodules  *bool `json:"submodules,omitempty"`
    LFS         *bool `json:"lfs,omitempty"`
}
```


### type StepRisks

StepRisks attributes refusal diagnostics to a job and step on every placement.

```
type StepRisks struct {
    JobID  string   `json:"job_id"`
    StepID string   `json:"step_id"`
    Labels []string `json:"labels"`
}
```


### type StepSpec

StepSpec carries author declarations, never a host-computed spec hash or lifecycle state.

```
type StepSpec struct {
    ID                string      `json:"id"`
    Needs             []string    `json:"needs,omitempty"`
    Groups            []string    `json:"groups,omitempty"`
    Finally           bool        `json:"finally,omitempty"`
    Risks             []string    `json:"risks,omitempty"`
    Optional          bool        `json:"optional,omitempty"`
    ContinueOnError   bool        `json:"continue_on_error,omitempty"`
    HasDryRun         bool        `json:"has_dry_run,omitempty"`
    SafeWithoutDryRun bool        `json:"safe_without_dry_run,omitempty"`
    Closures          ClosureSpec `json:"closures,omitzero"`
}
```


### type SummaryRequest

SummaryRequest replaces the bound job's or step's summary after masking and bounding.

```
type SummaryRequest struct {
    Markdown string `json:"markdown"`
    Step     string `json:"step,omitempty"`
}
```


### type Trigger

Trigger describes what started the run: Source is manual, push, schedule or webhook.

```
type Trigger struct {
    Source      string       `json:"source"`
    User        string       `json:"user,omitempty"`
    PullRequest *PullRequest `json:"pull_request,omitempty"`
}
```


### type UsageExample

UsageExample preserves author help without letting the SDK choose host command semantics.

```
type UsageExample struct {
    Comment string `json:"comment"`
    Command string `json:"command"`
}
```


### type Writer

Writer writes one JSON value per line.

```
type Writer struct {
    // contains filtered or unexported fields
}
```

- `func NewWriter(w io.Writer) *Writer` -- NewWriter writes lines to w.
- `func (w *Writer) Send(v any) error` -- Send writes v as one line.

## Constants

```
const (
    CacheResolverGoModules = "go-modules"
    CacheResolverNpm       = "npm"
)
```

```
const ExecTailBytes = 64 << 10
```

```
const HelloProtocol = 2
```

```
const MaxLineBytes = int(protolimits.NodeOutputBytes + protolimits.ProtocolEnvelopeBytes)
```

```
const MinJobCores = 0.25
```

```
const Version = 2
```

## Variables

```
var (

    // ErrMalformed reports a request or frame that does not have the
    // protocol's shape.
    ErrMalformed = errors.New("nodeproto: malformed line")
)
```
