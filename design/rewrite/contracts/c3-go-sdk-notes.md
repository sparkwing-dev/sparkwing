# Go SDK contract

The author SDK owns declarations, typed input and output access, protocol serving, and calls to host services. The host owns YAML, placement, concurrency keys, authorization, secret preflight, debug filtering, risk enforcement, state transitions, and storage. `sparkwingtest` owns isolated fake-host fixtures. `RuntimePlumbing`, including every exported context key and function field in `runtime_bridge.go`, is deleted from the target public surface.

`*_contract.go` declares the changed surface. Retain entries keep their author-facing signatures. Delete entries are excluded from the public API, including forwarding aliases.

## Invariants

- Resource options must preserve explicit presence internally. Cores and MemoryGB set that presence even when validation rejects the supplied value. The old zero-as-unset ResourceHints representation must not reach the plan document.
- `Checkout` pointer fields distinguish unset from explicit zero or false. `FullHistory=true` overrides Depth as in the retained author semantics. The host resolves absent fields and validates explicit values.
- A pipeline executable enters through `sparkwing.Main`. Its engine dependency graph is limited to the SDK, protocol and pipe transport. It must not import controller, store, runner orchestration, YAML, or secret-backend implementation packages. The implementation phase moves plan serialization into the SDK and removes the Delete exports below. This contract does not refactor that runtime graph.
- The Go constructor remains `Job`, returning `*JobNode`, because Go cannot declare a type and function both named `Job`. Customer IDs, records and documentation use job. `Ref.JobID`, `RunContext.NoMemo`, and the `LogRecord` job tag carry that vocabulary.
- `MemoValue`, `MemoKeyFn`, `MemoKey`, `MemoTTL`, and `NoMemo` concern result reuse. `Cache` concerns files and routes through depcache. Hosts own key scoping and trusted fallback. Executable-job SkipIf and memo checks run on the executing host under its actual award, inside its acquired concurrency slot. Empty memo keys fail. A missing or corrupt memo object is a miss. A depcache failure runs cold, an artifact staging failure fails, and a missing output read fails.
- `Artifacts` publishes files. `Consumes` adds a producer dependency and stages only authorized artifacts. Typed output remains `Produces`, `RefTo`, and `StepGet`, with the 16 MiB host limit.
- Generators run on successful and memoized source outputs. `GenerateFn` receives an expansion plan and reads source output through a typed reference. Each registration receives a stable group ID from source ID plus registration ordinal. `NeedsGenerated` waits for settlement and generated prerequisites. The host accepts at most 256 jobs per generator fan-out and persists the topology. Replay reconstructs code only and must reproduce the accepted spec hash. Failed, skipped, cancelled and superseded sources resolve the barrier without running the callback.
- Generator contexts and their SDK commands exclude the source job's body environment. Conditions, hooks and step bodies retain their own job's body environment. Replay cannot let source assignments change reconstructed declarations.
- Closure and generator IDs keep job IDs distinct from closure kinds and step IDs. A job condition on `scope/step/release` cannot collide with a step condition on job `scope`, step `release`. IDs containing literal percent escapes remain distinct from IDs containing slashes.
- `Env` is body-scoped by default. `Startup()` and the engine's fixed interpreter variable list take effect before the attempt process starts. Startup values must not influence declarations. Drift fails as `plan_drift`. Tools and cleanups inherit body environment. Credentials stay in memory or the protocol pipe.
- `Mask` acknowledges host registration before returning. Annotation levels are exactly `debug`, `info`, `warn`, `error`. Empty level means `info`. `File` is optional and line, end-line and column are one-based when supplied. Zero omits a position. Annotation text is masked before enforcing the 128 KiB limit. The host sets the job envelope and filters debug.
- Approvals execute no before/after job hooks. SkipIf remains an admission condition. Zero timeout remains indefinite on local and controller-owned runs, and a bounded owner's deadline still applies. `OnTimeout` uses `fail`, `deny`, or `approve`. `OnFailure` creates a separate executable recovery job.
- `JobNode.OnFailure` and `Approval.OnFailure` return the newly declared recovery job. Chained modifiers apply to that job, and another `OnFailure` declares its child. Recovery chains include every declared recovery job, with each `on_failure_of` naming its immediate parent. Nested jobs participate in argument binding, describe, closure lookup and execution. Generated recovery chains retain their source dependency and generator groups at every depth.
- Retry attempts stay under one claim. Step predicates and bodies consume the same explicit host `attempt` and `settled` snapshot. Each new node-wide attempt resets in-process step outputs and completion state before its first predicate or body. Parallel steps within one attempt share those outputs. Every terminal body outcome settles before releasing activity. Stale ordinals, incomplete terminal snapshots, repeated terminal steps, duplicate active steps or a new attempt overlapping active predicates/bodies fail with `protocol_error` before state changes. `RetryAuto` and the old `RetryConfig.Auto` field are outside the target. `RetryConfig` retains Attempts and Backoff only. The host refuses values above its ceiling rather than clamping.
- Final outcome constants are `Success`, `Failed`, `Cancelled`, `Skipped`, `Superseded`, `Memoized`, with values `success`, `failed`, `cancelled`, `skipped`, `superseded`, `memoized`. `Satisfied`, `Cached`, `SkippedConcurrent` and `Paused` are excluded. Reasons carry concurrency skips. `Outcome.Terminal` must reject unknown values. Step failure policy wire values are `fail_fast` and `collect_all`, and concurrency cancellation is `cancel_others`.
- Secrets are declared once in describe through `SecretsProvider.Secrets`, resolved lazily with `Secret` or `PipelineSecrets`. Plain values use `Variable` and `MustVariable`. Registered secret values cannot occur in job, step, group or approval IDs. The shared protocol validator enforces that rule before storing IDs.
- Node-host responses must carry their result fields. Secrets require an explicit string value and boolean masking classification, preserving empty values and `false`. OIDC and cleanup registration require a nonempty token or ID. Output, latest-output and successful child responses require an output member, which may be JSON null. Missing fields are transport failures, never optional-reference absence. Additive response fields remain readable.
- `WithArgs` fields on ordinary and recovery jobs share the pipeline's flag namespace and input validation. Describe includes their defaults and owning job IDs. Required fields, enums and name collisions are checked before execution, and recovery bodies receive the resolved values.
- Describe builds declarations with pipeline defaults and initialized extra-argument bags, separately from execution input validation. Required run inputs do not erase job metadata. Declaration errors and panics fail discovery with a pipeline-attributed, secret-masked canonical error. Describe never returns a successful partial schema. Execution still validates all required pipeline and job arguments.
- Generated jobs bind `WithArgs` from the original run argument snapshot before their expansion becomes executable. Defaults, required values and enums apply to generated recovery jobs too. Their flags cannot collide with pipeline inputs, initial jobs or prior expansions. Flags introduced only during generation can arrive through the pipeline's `flag:",extra"` bag. Describe does not execute generators to discover them.
- Cross-pipeline `Ref.TryGet` treats JSON null as absent, including null surrounded by JSON whitespace. `Ref.Get` returns the zero value. An in-run null output remains present, and the JSON string `"null"` remains a value. Invalid JSON fails instead of becoming absence.
- `sparkwingtest.Host.Context` installs only the fixture's runner, inputs, secret classification and service answers. `Plan`, `RunStep` and `Eval` exercise SDK operations without controller/store imports. Planning has no execution binding, so secret, variable, output, latest-output, child, OIDC and Mask calls fail before reading fixture answers or invoking callbacks. Declaration logs remain available. RunStep records carry the host-bound job. Plan and eval records go to run logs without a job. Records and masks are snapshots. Cancelled service calls never invoke fixture callbacks. `Close` cancels outstanding fake requests and releases resources. Fixtures share no mutable global state.

## Dependency cache keys

`DirCache.Key(workdir string) (string, error)` exposes dependency-input hashing
without importing engine packages. It reads the first present declared key file
through a directory root that rejects escaping symlinks. Absolute paths and
parent traversal fail. The hash covers the cache name, key scope, OS, architecture
and exact file bytes. Moving the checkout preserves it. Host authorization still
selects the repository and ref namespace. This helper grants no storage access.

## Service records and fixture validation

The Go protocol transport sends Mask, annotation and summary writes through the
shared node-host service routes. Each write waits for an object acknowledgement.
A record write error fails its body or evaluation with the canonical error.
Ordinary logs continue over the frame pipe.

Host-service identifiers travel as opaque URL path segments. The SDK escapes
dots as well as separators so `.` and `..` cannot trigger path cleaning.
Receivers match the escaped segments and unescape each identifier once.
An encoded slash remains one identifier, including when the whole value is `/`.

Annotations require an execution context. The fake host refuses annotation and summary
writes after fixture cancellation, including writes from retained detached contexts.
Refused writes add no records. Eval records remain in run logs without a job.

The fake host retains accepted declaration snapshots. It refuses a dynamic mask
that occurs in an accepted identifier before changing its registry. Later plans
and generated declarations use that same registry. Acceptance and registration
share one fixture lock so neither can race past the other.
Mask registration also rechecks fixture cancellation under that lock, so a call
admitted before Close cannot commit after Close returns.

The fake host refuses an empty OIDC callback result, matching the node-host
transport's nonempty token requirement.

Closing a fake host cancels and joins admitted plan, step, eval and service callback
calls. Active callbacks remain cancellable when callers use `context.WithoutCancel`.
New calls fail after closing begins, including service calls whose contexts detach
cancellation from the fixture. Close runs outside those calls.

The shared v2 corpus at `internal/nodeproto/conformance/testdata/v2-corpus.json`
contains declaration and error vectors in addition to process sessions. Its
`plans` rows include raw JSON, canonical JSON, SHA-256, expected validation code,
and any expansion or secret context. `inner_errors` supplies the shared decoder
cases. W4's reader checks them against structural validation and the canonical
hash owner. Other SDKs consume these same rows for parity.
Go builders also reproduce the source-presence, approval-condition and snake_case
job vectors. Jobs and steps omit empty closure objects, preserving their shared
canonical hashes. The Go process corpus uses the native acceptance pipe driver
on Unix and Windows.
Process session steps can name a `plan_vector` for generator results. The Go
driver validates those jobs against the initial plan and compares their canonical
hash with that vector. Repeated evaluation and a fresh process reconstruct the
same expansion. The fresh process carries the generated job's delegated binding
and executes its body after reconstruction. Every eval also checks its declared
value kind through `EvalResult.ValidateValue`.

## Acceptance evidence required

| Scenario | Contract proved |
|---|---|
| Compile a minimal registered pipeline using only `sparkwing.Main` | Lightweight dependency boundary and retained author API |
| Equivalent Go and TS plans for each public builder | Plan fields, enum spellings and semantic parity |
| Memo hit restores typed output and artifact manifest | Result reuse differs from depcache files |
| Fake host supplies outputs, secrets, runner identity and OIDC | Unit tests need no runtime plumbing exports |
| Startup and body env use the same input plan | Body env cannot affect plan declarations, startup drift fails |
| Generate after success and memoized source, then replay after owner loss | Persisted expansion authority and deterministic code reconstruction |
| Source failure, cancellation and empty expansion | No stranded generated dependency barrier |
| Approval zero timeout and all timeout policies | No new approval deadline and no executable approval hooks |
| Mask acknowledgement followed by log, annotation and summary | No registration race, correct masking and host envelope |
| Secret-bearing job, step, group and approval IDs | Decision 90 refusal without exposing the secret in diagnostics |
| Retry with Backoff and no auto option | Application retries retain the claim |
| Older pinned SDK and active signed security policy | Exact matching engine and twelve-month controller API compatibility under D102-103; actionable revocation when required |

## Contract interpretations

Amendment 1 ratifies the interpretations below. They are not approval gates.

1. §3.2/A27 requires parity for approval plus SkipIf, while A28 and the chapter cut approval hooks. This contract keeps SkipIf as an admission condition and removes executable BeforeRun/AfterRun hooks, matching the shared validator and the all-features launch scope. Amendment 1 ratifies that distinction under A28 and decision 99.
2. `JobNode` is the retained Go handle name because `Job` is the constructor. Renaming the handle would require a new name absent from the frozen design.
## Cross-area assumptions

- `nodeproto.PlanDoc` is the fake host's inferred plan result. Eval accepts public SDK plans and raw JSON so external authors need no internal-package imports. C3 owns the strict wire and C2 owns structured errors.
- C4 resolves `Startup` and automatic interpreter variables with the same startup/body rules, and owns ceilings. SDK declarations do not resolve settings.
- C1 implements memoized outcomes, artifact/reference authority and expansion settlement. SDK callbacks never write topology or state directly.
- C2 supplies acknowledged mask registration and secret/variable authorization through node-host routes. The SDK stores the hello bearer only in memory.

`Host.Plan` takes the registered SDK entrypoint as its name argument. A nonempty `Options.Run.Pipeline` supplies the invoked project alias to author code. When unset, the entrypoint is also the invoked name.

`sparkwingtest.New` snapshots option values, including Git and pull-request metadata. Reusing and changing an options value for another fixture cannot change an existing fixture's plans. Injected service callbacks remain test-owned dependencies.

CLI discovery, production planning and the test host share one declaration gate.
Incomplete discovery withholds author diagnostics through subsequent planning.
Directly constructed test plans register argument declarations before execution.
Test-host step/eval outputs containing a secret are withheld like production.
Stored secrets, supplied secret args and selected nonempty defaults follow the
four-character refusal. Secrets containing `***` are accepted. Empty supplied
arguments and defaults carry no value. Unselected defaults do not refuse
a run. Their valid raw values and list items register while short items are
skipped. Invalid derived proxy parts are skipped.
Runs without version 1 host registrations cannot resume and must be started again.

Fake-host records use the host masking module. It masks serialized JSON attributes, validates the result including duplicate keys, and withholds the whole attribute payload on failure. Text streams are masked before line splitting. Pipeline code run without a host is unmasked.

Host planning diagnostics use the shared matcher for overlapping secret values
and defaults. Private declarations drive execution lookup and argument parsing.
Help and cached descriptions use the separate presentation type, which withholds
all unclassified text after incomplete discovery.

## Public symbol inventory

Each row names one exported type or function. Retain means its declared signature is part of the target. New means its declaration is in a contract file. Delete means engine plumbing or a removed author feature. Types retain their author-facing fields except the field cuts stated above. Constructors and fluent methods retain their existing invalid-input behavior unless a host-owned validation rule above supersedes it.


### New

| Symbol | Source | Purpose |
|---|---|---|
| `inputs.RepoFiles` | `sparkwing/inputs/memo_contract.go` | RepoFiles uses tracked content so transient build outputs cannot change a memo key. |
| `inputs.Files` | `sparkwing/inputs/memo_contract.go` | Files hashes matching tracked content and fails on invalid patterns. |
| `inputs.Tree` | `sparkwing/inputs/memo_contract.go` | Tree hashes regular files without following symlinks outside the requested tree. |
| `inputs.Env` | `sparkwing/inputs/memo_contract.go` | Env distinguishes an unset name from an explicitly empty value. |
| `inputs.Const` | `sparkwing/inputs/memo_contract.go` | Const permits author-owned stable keys while empty keys remain invalid. |
| `inputs.Compose` | `sparkwing/inputs/memo_contract.go` | Compose propagates errors and NoMemo without silently hashing invalid input. |
| `sparkwing.Main` | `sparkwing/sdk_contract.go` | Main keeps a pipeline executable independent of the controller and store. |
| `sparkwing.MemoValue` | `sparkwing/sdk_contract.go` | MemoValue separates job-result reuse from dependency file caches. |
| `sparkwing.MemoKeyFn` | `sparkwing/sdk_contract.go` | MemoKeyFn evaluates author-owned inputs before the host scopes their key. |
| `sparkwing.MemoOption` | `sparkwing/sdk_contract.go` | MemoOption keeps unset TTL distinct from an explicit duration. |
| `sparkwing.MemoKey` | `sparkwing/sdk_contract.go` | MemoKey hashes ordered inputs; callers resolve references before hashing. |
| `sparkwing.MemoTTL` | `sparkwing/sdk_contract.go` | MemoTTL declares retention subject to the host's ceiling at plan acceptance. |
| `sparkwing.JobNode.Memo` | `sparkwing/sdk_contract.go` | Memo declares result reuse; missing or corrupt cached bytes produce a miss. |
| `sparkwing.JobGroup.Memo` | `sparkwing/sdk_contract.go` | Memo applies one reuse policy to each static or generated group member. |
| `sparkwing.Backoff` | `sparkwing/sdk_contract.go` | Backoff declares retry delay without selecting an execution placement. |
| `sparkwing.JobNode.Cache` | `sparkwing/sdk_contract.go` | Cache declares files restored through the host-authorized depcache scope. |
| `sparkwing.JobGroup.Cache` | `sparkwing/sdk_contract.go` | Cache applies dependency-file declarations to every group member. |
| `sparkwing.Npm` | `sparkwing/sdk_contract.go` | Npm requests npm's download cache without introducing a package proxy. |
| `sparkwing.JobNode.Artifacts` | `sparkwing/sdk_contract.go` | Artifacts distinguishes published files from the job's typed output. |
| `sparkwing.JobGroup.Artifacts` | `sparkwing/sdk_contract.go` | Artifacts applies published-file patterns to every group member. |
| `sparkwing.JobNode.NeedsIfPresent` | `sparkwing/sdk_contract.go` | NeedsIfPresent ignores absent dependencies but preserves failure ordering for present jobs. |
| `sparkwing.JobGroup.NeedsIfPresent` | `sparkwing/sdk_contract.go` | NeedsIfPresent applies presence-sensitive ordering to every group member. |
| `sparkwing.GenerateFn` | `sparkwing/sdk_contract.go` | GenerateFn reconstructs executable declarations from the source's persisted output |
| `sparkwing.Generator` | `sparkwing/sdk_contract.go` | Generator identifies a source expansion whose settlement precedes dependent jobs. |
| `sparkwing.JobNode.Generate` | `sparkwing/sdk_contract.go` | Generate declares an expansion after source success or memo reuse; the host owns acceptance. |
| `sparkwing.Generator.ID` | `sparkwing/sdk_contract.go` | ID exposes the stable group identifier used in persisted expansion identity. |
| `sparkwing.JobNode.NeedsGenerated` | `sparkwing/sdk_contract.go` | NeedsGenerated waits for expansion settlement and all generated prerequisites. |
| `sparkwing.JobGroup.NeedsGenerated` | `sparkwing/sdk_contract.go` | NeedsGenerated carries the same expansion barrier to every group member. |
| `sparkwing.EnvOption` | `sparkwing/sdk_contract.go` | EnvOption distinguishes process startup settings from body-only environment. |
| `sparkwing.Startup` | `sparkwing/sdk_contract.go` | Startup marks interpreter configuration that must never influence plan declarations. |
| `sparkwing.JobNode.Env` | `sparkwing/sdk_contract.go` | Env defaults to body scope; startup declarations take effect before attempt process creation. |
| `sparkwing.JobGroup.Env` | `sparkwing/sdk_contract.go` | Env applies the same startup/body distinction to all members. |
| `sparkwing.ApprovalConfig` | `sparkwing/sdk_contract.go` | ApprovalConfig preserves indefinite waiting when Timeout is zero. |
| `sparkwing.Approval` | `sparkwing/sdk_contract.go` | Approval exposes only declarative gate behavior because gates execute no job hooks. |
| `sparkwing.Plan.Approval` | `sparkwing/sdk_contract.go` | Approval declares a manual gate whose owner enforces waiting and timeout policy. |
| `sparkwing.Approval.ID` | `sparkwing/sdk_contract.go` | ID permits gate references without exposing executable-job internals. |
| `sparkwing.Approval.Needs` | `sparkwing/sdk_contract.go` | Needs waits for prerequisite jobs before requesting a decision. |
| `sparkwing.Approval.NeedsIfPresent` | `sparkwing/sdk_contract.go` | NeedsIfPresent permits plans whose optional prerequisite is absent. |
| `sparkwing.Approval.NeedsGenerated` | `sparkwing/sdk_contract.go` | NeedsGenerated keeps a gate behind the persisted expansion barrier. |
| `sparkwing.Approval.SkipIf` | `sparkwing/sdk_contract.go` | Conditions gate admission after dependencies without executable before/after hooks. |
| `sparkwing.Approval.Optional` | `sparkwing/sdk_contract.go` | Optional excludes a denied or expired gate from required run success. |
| `sparkwing.Approval.ContinueOnError` | `sparkwing/sdk_contract.go` | ContinueOnError permits dependents after a gate failure. |
| `sparkwing.Approval.OnFailure` | `sparkwing/sdk_contract.go` | OnFailure declares an executable recovery job separate from the gate. |
| `sparkwing.Work.FailurePolicy` | `sparkwing/sdk_contract.go` | FailurePolicy chooses fail_fast or collect_all without changing dependency requirements. |
| `sparkwing.Annotation` | `sparkwing/sdk_contract.go` | Annotation carries source location and the same severity vocabulary as log records. |
| `sparkwing.Annotate` | `sparkwing/sdk_contract.go` | Annotate preserves structured location while the host masks and enforces the 128 KiB limit. |
| `sparkwing.Mask` | `sparkwing/sdk_contract.go` | Mask returns only after host registration so subsequent output cannot race masking. |
| `sparkwing.Variable` | `sparkwing/sdk_contract.go` | Variable distinguishes a declared unmasked value from a secret. |
| `sparkwing.MustVariable` | `sparkwing/sdk_contract.go` | MustVariable makes a missing required plain value fail the job. |
| `sparkwingtest.Secret` | `sparkwing/sparkwingtest/host_contract.go` | Secret preserves masking classification so tests cannot substitute plain variables for secrets. |
| `sparkwingtest.Options` | `sparkwing/sparkwingtest/host_contract.go` | Options injects external results without exporting runtime plumbing from the author SDK. |
| `sparkwingtest.Host` | `sparkwing/sparkwingtest/host_contract.go` | Host exercises author code through the same plan and operation contract as a real host. |
| `sparkwingtest.New` | `sparkwing/sparkwingtest/host_contract.go` | New isolates fixtures so concurrent tests cannot share secrets, outputs or protocol state. |

### Retain

| Symbol | Source | Purpose |
|---|---|---|
| `sparkwing.AdmissionClass` | `sparkwing/admission.go` | AdmissionClass describes the latency expectations of a pipeline without imposing an absolute queue order. |
| `sparkwing.Plan.AdmissionClass` | `sparkwing/admission.go` | Declares run latency expectations for host admission. |
| `sparkwing.ConsumeOption` | `sparkwing/artifacts.go` | ConsumeOption tunes a JobNode.Consumes declaration. |
| `sparkwing.Into` | `sparkwing/artifacts.go` | Into stages the consumed producer's artifacts under prefix, with the producer's internal structure preserved. |
| `sparkwing.JobNode.Consumes` | `sparkwing/artifacts.go` | Consumes declares that this job stages the artifacts produced by producer into its workspace before it runs, and implies Needs(producer). |
| `sparkwing.WorkStep.Risk` | `sparkwing/blast_radius.go` | Risk marks a WorkStep as gated by the given operator-acknowledged labels. |
| `sparkwing.Checkout` | `sparkwing/sdk_contract.go` | Declares optional source history, tags, submodules and LFS settings. |
| `sparkwing.Plan.Checkout` | `sparkwing/sdk_contract.go` | Declares source preferences for host resolution. |
| `cleanup.Spec` | `sparkwing/cleanup/cleanup.go` | Spec describes a cleanup to run if the registering job dies first. |
| `cleanup.Register` | `sparkwing/cleanup/cleanup.go` | Register records a cleanup that runs if the registering process exits before the library calls the release it returns. |
| `sparkwing.JobGroup` | `sparkwing/combinator.go` | JobGroup holds jobs for dependency wiring and dashboard grouping. |
| `sparkwing.JobGroup.Name` | `sparkwing/combinator.go` | Name returns the group's declared name, or "" for an unnamed (structural-only) group. |
| `sparkwing.JobGroup.Members` | `sparkwing/combinator.go` | Members returns the group's current jobs. |
| `sparkwing.GroupJobs` | `sparkwing/combinator.go` | GroupJobs groups existing Plan jobs under name and returns a dependency target covering every member. |
| `sparkwing.JobFanOut` | `sparkwing/combinator.go` | JobFanOut registers one job per item during plan construction. |
| `sparkwing.JobGroup.Needs` | `sparkwing/combinator.go` | Needs declares an upstream dependency on every member of the group. |
| `sparkwing.JobGroup.Retry` | `sparkwing/combinator.go` | Retry configures every member to be re-attempted up to attempts additional times on failure. |
| `sparkwing.JobGroup.Timeout` | `sparkwing/combinator.go` | Timeout caps the per-attempt duration on every member. |
| `sparkwing.JobGroup.NoProgressTimeout` | `sparkwing/combinator.go` | NoProgressTimeout sets the per-attempt inactivity timeout on every member. |
| `sparkwing.JobGroup.Verify` | `sparkwing/combinator.go` | Verify registers a postcondition check on every member. |
| `sparkwing.JobGroup.Consumes` | `sparkwing/combinator.go` | Consumes stages the given producer's artifacts into every member's workspace before it runs, and implies Needs(producer) on each. |
| `sparkwing.JobGroup.Requires` | `sparkwing/combinator.go` | Requires constrains runner claims for every non-inline dispatched member. |
| `sparkwing.JobGroup.NeedsTools` | `sparkwing/combinator.go` | Requires the named tool labels on runners for every member. |
| `sparkwing.JobGroup.Prefers` | `sparkwing/combinator.go` | Applies soft runner-label preferences to every member. |
| `sparkwing.JobGroup.SkipIf` | `sparkwing/combinator.go` | SkipIf registers a predicate on every member. |
| `sparkwing.JobGroup.Inline` | `sparkwing/combinator.go` | Requests run-host execution for every member. |
| `sparkwing.JobGroup.ContinueOnError` | `sparkwing/combinator.go` | ContinueOnError marks every member so downstream dependents proceed even on failure. |
| `sparkwing.JobGroup.Optional` | `sparkwing/combinator.go` | Optional marks every member as non-essential. |
| `sparkwing.JobGroup.BeforeRun` | `sparkwing/combinator.go` | BeforeRun registers a pre-run hook on every member. |
| `sparkwing.JobGroup.AfterRun` | `sparkwing/combinator.go` | AfterRun registers a post-run hook on every member. |
| `sparkwing.Scope` | `sparkwing/concurrency.go` | Scope selects how far a ConcurrencyGroup's budget reaches: only the jobs of one run, every run on one machine, or the whole fleet coordinating through a shared backend. |
| `sparkwing.OnLimit` | `sparkwing/concurrency.go` | OnLimit is the closed set of behaviors for a job that finds its ConcurrencyGroup at capacity. |
| `sparkwing.ConcurrencyLimit` | `sparkwing/concurrency.go` | ConcurrencyLimit is the budget a ConcurrencyGroup enforces. |
| `sparkwing.ConcurrencyGroup` | `sparkwing/concurrency.go` | ConcurrencyGroup is a named budget that member jobs share. |
| `sparkwing.NewConcurrencyGroup` | `sparkwing/concurrency.go` | NewConcurrencyGroup constructs a ConcurrencyGroup named name with the given limit. |
| `sparkwing.ConcurrencyGroup.Name` | `sparkwing/concurrency.go` | Returns the author-declared group name, before host scoping. |
| `sparkwing.ConcurrencyGroup.Limit` | `sparkwing/concurrency.go` | Limit returns the group's declared budget. |
| `sparkwing.JobNode.Concurrency` | `sparkwing/concurrency.go` | Concurrency enrolls the job in concurrency group g with the given admission cost (default 1). |
| `sparkwing.JobGroup.Concurrency` | `sparkwing/concurrency.go` | Concurrency enrolls every member of the group in concurrency group g with the given admission cost. |
| `sparkwing.Plan.Concurrency` | `sparkwing/concurrency.go` | Concurrency gates the whole run on concurrency group g: the run acquires each declared plan-level budget before any job dispatches and releases it when the run reaches a terminal status. |
| `sparkwing.RunContext` | `sparkwing/context.go` | Carries host-supplied run metadata and declared execution modes. |
| `sparkwing.TriggerInfo` | `sparkwing/context.go` | TriggerInfo describes the trigger that started the run. |
| `sparkwing.PullRequest` | `sparkwing/context.go` | PullRequest carries the GitHub pull_request fields a PR gate reads. |
| `sparkwing.LogRecord` | `sparkwing/context.go` | LogRecord is the structured unit every Logger receives. |
| `sparkwing.Logger` | `sparkwing/context.go` | Logger is the sink for job output. |
| `sparkwing.Info` | `sparkwing/context.go` | Info emits an info-level message to the active logger. |
| `sparkwing.Warn` | `sparkwing/context.go` | Warn emits a warn-level message. |
| `sparkwing.Error` | `sparkwing/context.go` | Formats the error for diagnostics. |
| `sparkwing.Summary` | `sparkwing/context.go` | Summary records a persistent markdown run summary on the currently-executing Job or Step. |
| `sparkwing.Debug` | `sparkwing/debug.go` | Debug emits a debug-level LogRecord. |
| `sparkwing.DirCache` | `sparkwing/depcache.go` | DirCache describes one directory a job restores before running and saves after succeeding: a dependency directory keyed by the content of the lockfile that produced it. |
| `sparkwing.KeySource` | `sparkwing/depcache.go` | KeySource names the file whose content keys a Dir cache. |
| `sparkwing.KeyFromFile` | `sparkwing/depcache.go` | KeyFromFile keys a Dir cache on the content of one file, typically a lockfile. |
| `sparkwing.GoModules` | `sparkwing/depcache.go` | GoModules caches the Go module download cache, keyed on go.sum. |
| `sparkwing.Dir` | `sparkwing/depcache.go` | Declares a dependency-file cache with an explicit key source. |
| `sparkwing.HelpProvider` | `sparkwing/describe.go` | HelpProvider is optionally implemented by pipelines to contribute a short description to sparkwing run <name> --help. |
| `sparkwing.ShortHelpProvider` | `sparkwing/describe.go` | ShortHelpProvider is optionally implemented by pipelines to contribute a one-line hint (<=80 chars, no trailing period) for tab completion and list views. |
| `sparkwing.ExampleProvider` | `sparkwing/describe.go` | ExampleProvider is optionally implemented by pipelines to contribute worked invocations to sparkwing run <name> --help. |
| `sparkwing.Example` | `sparkwing/describe.go` | Example is a single help-screen invocation. |
| `sparkwing.EnvVarDocer` | `sparkwing/describe.go` | EnvVarDocer is an optional interface a Pipeline can implement to declare environment variables it reads as inputs. |
| `sparkwing.EnvVarDoc` | `sparkwing/describe.go` | EnvVarDoc is one declared env var read by a pipeline. |
| `docker.BuildConfig` | `sparkwing/docker/docker.go` | BuildConfig configures a Docker image build and optional push. |
| `docker.BuildResult` | `sparkwing/docker/docker.go` | BuildResult is returned by Build / BuildAndPush. |
| `docker.Build` | `sparkwing/docker/docker.go` | Build runs docker build (or docker buildx build when Platforms is non-empty) and applies the tags locally. |
| `docker.BuildAndPush` | `sparkwing/docker/docker.go` | BuildAndPush builds the image (same rules as Build) and pushes it to every configured registry. |
| `docker.Push` | `sparkwing/docker/docker.go` | Push tags and pushes the given local image reference to each registry with every requested tag. |
| `docker.Login` | `sparkwing/docker/docker.go` | Login authenticates docker with the given registry. |
| `docker.BuildxPlatforms` | `sparkwing/docker/docker.go` | Lists the platforms supported by the active buildx builder. |
| `docker.FilterBuildxPlatforms` | `sparkwing/docker/docker.go` | FilterBuildxPlatforms returns the subset of wish that the active buildx builder can build. |
| `docker.RunOptions` | `sparkwing/docker/run.go` | RunOptions configures a one-shot container invocation. |
| `docker.Run` | `sparkwing/docker/run.go` | Run executes Opts.Cmd inside a one-shot Opts.Image container. |
| `docker.ImageTag` | `sparkwing/docker/tags.go` | ImageTag holds the deterministic image-tag components for a single build: short commit SHA, content hash of the build's fileset, and a dirty bit. |
| `docker.ImageTag.DeployTag` | `sparkwing/docker/tags.go` | DeployTag is the canonical content-addressed tag applied to every build. |
| `docker.ImageTag.ProdTag` | `sparkwing/docker/tags.go` | Returns the deployment tag with the production suffix. |
| `docker.ImageTag.All` | `sparkwing/docker/tags.go` | Returns the deployment and production image tags. |
| `docker.ComputeTags` | `sparkwing/docker/tags.go` | ComputeTags reads the git repo state at the process CWD and returns an ImageTag describing it. |
| `docker.ComputeTagsIn` | `sparkwing/docker/tags.go` | ComputeTagsIn reads the git repo state in repoDir and returns an ImageTag describing it. |
| `sparkwing.IsDryRun` | `sparkwing/dry_run.go` | IsDryRun reports whether ctx is in dry-run mode. |
| `sparkwing.WorkStep.DryRun` | `sparkwing/dry_run.go` | DryRun installs a dry-run body on this WorkStep. |
| `sparkwing.WorkStep.SafeWithoutDryRun` | `sparkwing/dry_run.go` | Declares that the ordinary step body is safe during a dry run. |
| `sparkwing.WorkDir` | `sparkwing/exec.go` | WorkDir returns the pipeline working directory (the repo root). |
| `sparkwing.ExecResult` | `sparkwing/exec.go` | ExecResult is the structured result of a command invocation. |
| `sparkwing.ExecError` | `sparkwing/exec.go` | ExecError is returned when a command exits non-zero. |
| `sparkwing.ExecError.Error` | `sparkwing/exec.go` | Formats the error for diagnostics. |
| `sparkwing.ExecError.Unwrap` | `sparkwing/exec.go` | Exposes the underlying failure to errors.Is and errors.As. |
| `sparkwing.Cmd` | `sparkwing/exec.go` | Cmd is the chainable command builder returned by Bash and Exec. |
| `sparkwing.WithCommandEnv` | `sparkwing/exec.go` | WithCommandEnv returns a context whose sparkwing.Exec/Bash calls inherit env. |
| `sparkwing.Bash` | `sparkwing/exec.go` | Bash starts building a shell command (run via "bash -c"). |
| `sparkwing.Exec` | `sparkwing/exec.go` | Exec starts building an argv command (no shell). |
| `sparkwing.Cmd.Dir` | `sparkwing/exec.go` | Dir sets the working directory for the command. |
| `sparkwing.Cmd.Env` | `sparkwing/exec.go` | Env adds (or overrides) a single environment variable. |
| `sparkwing.Cmd.EnvMap` | `sparkwing/exec.go` | EnvMap merges a map of environment variables into the command. |
| `sparkwing.Cmd.Run` | `sparkwing/exec.go` | Run executes the command, streaming stdout/stderr line-by-line to the logger installed in ctx. |
| `sparkwing.Cmd.Capture` | `sparkwing/exec.go` | Capture executes the command silently -- no per-line log records, just the exec_start banner. |
| `sparkwing.Cmd.String` | `sparkwing/exec.go` | String runs the command silently and returns TrimSpace(stdout). |
| `sparkwing.Cmd.Lines` | `sparkwing/exec.go` | Lines runs the command silently and returns stdout split on "\n", with each line trimmed and blanks dropped. |
| `sparkwing.Cmd.JSON` | `sparkwing/exec.go` | JSON runs the command silently and decodes stdout into out via encoding/json. |
| `sparkwing.Cmd.MustBeEmpty` | `sparkwing/exec.go` | MustBeEmpty runs the command silently and returns nil only if its stdout (after TrimSpace) is empty. |
| `sparkwing.Path` | `sparkwing/fs.go` | Path joins parts onto WorkDir() and returns the absolute path. |
| `sparkwing.ReadFile` | `sparkwing/fs.go` | ReadFile reads the named file. |
| `sparkwing.WriteFile` | `sparkwing/fs.go` | WriteFile writes data to the named file with perm 0o644 (creating or truncating). |
| `sparkwing.Glob` | `sparkwing/fs.go` | Glob expands a shell-style glob pattern. |
| `git.CloneOption` | `sparkwing/git/clone.go` | CloneOption configures optional git-clone behavior. |
| `git.WithDepth` | `sparkwing/git/clone.go` | WithDepth limits the clone to the most recent n commits (--depth n). |
| `git.Clone` | `sparkwing/git/clone.go` | Clone clones url into destDir. |
| `git.Fetch` | `sparkwing/git/clone.go` | Fetch runs git fetch in repoDir. |
| `git.ShortCommit` | `sparkwing/git/git.go` | ShortCommit returns the HEAD commit SHA in repoDir truncated to 12 characters. |
| `git.CurrentSHA` | `sparkwing/git/git.go` | CurrentSHA returns the full HEAD commit SHA in repoDir. |
| `git.CurrentBranch` | `sparkwing/git/git.go` | CurrentBranch returns the branch name HEAD points at in repoDir. |
| `git.DefaultBranch` | `sparkwing/git/git.go` | DefaultBranch returns the repo's default branch name (typically "main" or "master"). |
| `git.RemoteOriginURL` | `sparkwing/git/git.go` | RemoteOriginURL returns the URL of origin in repoDir, or "" with nil error when no origin remote is configured. |
| `git.IsDirty` | `sparkwing/git/git.go` | IsDirty reports whether the working tree in repoDir has uncommitted changes -- either unstaged or staged-but-not-committed. |
| `git.FilesetHash` | `sparkwing/git/git.go` | FilesetHash returns a deterministic 12-char hex hash derived from the contents of every file that would land in a Docker build context for repoDir. |
| `git.ChangedFiles` | `sparkwing/git/git.go` | ChangedFiles returns paths (repo-relative) modified between since and HEAD in repoDir. |
| `git.TagsAtHead` | `sparkwing/git/git.go` | TagsAtHead returns every tag pointing at HEAD in repoDir. |
| `git.LatestTag` | `sparkwing/git/git.go` | LatestTag returns the highest semver tag in repoDir matching the given prefix, or "" if none. |
| `git.TagExistsOnRemote` | `sparkwing/git/git.go` | TagExistsOnRemote checks whether tag exists on origin in repoDir. |
| `git.PushTag` | `sparkwing/git/git.go` | PushTag creates an annotated tag locally in repoDir and pushes it to origin. |
| `sparkwing.Git` | `sparkwing/git_runtime.go` | Git is the run-scoped view of a single git working tree. |
| `sparkwing.NewGit` | `sparkwing/git_runtime.go` | NewGit constructs a Git with the supplied data fields and workDir. |
| `sparkwing.NewGitFromTree` | `sparkwing/git_runtime.go` | NewGitFromTree builds a Git by shelling out to inspect workDir. |
| `sparkwing.Git.WorkDir` | `sparkwing/git_runtime.go` | WorkDir returns the absolute path of the working tree this Git describes, or "" when constructed without a real clone. |
| `sparkwing.Git.ShortSHA` | `sparkwing/git_runtime.go` | ShortSHA returns g.SHA truncated to 12 chars; safe to call on a nil receiver (returns ""). |
| `sparkwing.Git.Name` | `sparkwing/git_runtime.go` | Name returns the bare repo name (the part after the last "/" in Repo). |
| `sparkwing.Git.IsDirty` | `sparkwing/git_runtime.go` | IsDirty reports whether the working tree has uncommitted changes. |
| `sparkwing.Git.FilesetHash` | `sparkwing/git_runtime.go` | FilesetHash returns a deterministic hash of every tracked file's content. |
| `sparkwing.Git.ChangedFiles` | `sparkwing/git_runtime.go` | ChangedFiles returns repo-relative paths modified between since and HEAD. |
| `sparkwing.Git.TagsAtHead` | `sparkwing/git_runtime.go` | TagsAtHead returns every tag pointing at HEAD. |
| `sparkwing.Git.LatestTag` | `sparkwing/git_runtime.go` | LatestTag returns the highest semver tag with prefix. |
| `sparkwing.Git.PushTag` | `sparkwing/git_runtime.go` | PushTag creates the annotated tag locally and pushes it to origin. |
| `sparkwing.GithubOwnerRepo` | `sparkwing/git_runtime.go` | GithubOwnerRepo splits a "owner/name" slug into its parts. |
| `inputs.RepoFilesOption` | `sparkwing/inputs/inputs.go` | Configures tracked-file memo input selection. |
| `inputs.Ignore` | `sparkwing/inputs/inputs.go` | Ignore excludes matching tracked paths from RepoFiles. |
| `sparkwing.Inputs` | `sparkwing/inputs_accessor.go` | Reads the current run's resolved typed pipeline arguments. |
| `sparkwing.Pipeline` | `sparkwing/inputs_schema.go` | Pipeline is the canonical pipeline shape: every pipeline declares a typed Inputs struct and populates a Plan that the SDK constructs and passes in. |
| `sparkwing.NoInputs` | `sparkwing/inputs_schema.go` | NoInputs is the empty-struct convention for pipelines that take no flags. |
| `sparkwing.OIDCToken` | `sparkwing/oidc.go` | OIDCToken returns an ID token the controller signed for this run, with aud set to audience. |
| `sparkwing.Outcome` | `sparkwing/outcome.go` | Outcome is a terminal job state. |
| `sparkwing.Outcome.Terminal` | `sparkwing/outcome.go` | Recognizes members of the final terminal outcome set. |
| `sparkwing.Outcome.OK` | `sparkwing/outcome.go` | OK reports whether the outcome satisfies downstream dependencies. |
| `sparkwing.Base` | `sparkwing/pipeline.go` | Base is the marker embedded by every pipeline. |
| `sparkwing.InputError` | `sparkwing/pipeline.go` | Reports invalid pipeline or job arguments. |
| `sparkwing.InputError.Error` | `sparkwing/pipeline.go` | Formats the error for diagnostics. |
| `sparkwing.InputError.Unwrap` | `sparkwing/pipeline.go` | Exposes the underlying failure to errors.Is and errors.As. |
| `sparkwing.Register` | `sparkwing/pipeline.go` | Register installs a pipeline under the given name. |
| `sparkwing.RegisterEntrypoint` | `sparkwing/pipeline.go` | Registers the SDK entry name that the host may select from project configuration. |
| `sparkwing.RunAndAwait` | `sparkwing/pipeline_await.go` | RunAndAwait triggers a fresh run of pipeline and waits for it to reach terminal state, returning the typed output of job ID from that run. |
| `sparkwing.WithFreshInputs` | `sparkwing/pipeline_await.go` | WithFreshInputs flattens a typed Inputs struct into the underlying args map. |
| `sparkwing.AwaitOption` | `sparkwing/pipeline_await.go` | AwaitOption tunes RunAndAwait's trigger + wait behavior. |
| `sparkwing.WithFreshTimeout` | `sparkwing/pipeline_await.go` | WithFreshTimeout bounds the total wait, including admission queue time. |
| `sparkwing.WithFreshArgs` | `sparkwing/pipeline_await.go` | Supplies arguments to a fresh child run. |
| `sparkwing.WithFreshRepo` | `sparkwing/pipeline_await.go` | Selects the child pipeline repository, subject to host authorization. |
| `sparkwing.WithFreshBranch` | `sparkwing/pipeline_await.go` | Selects the child run branch, without asserting verified-branch trust. |
| `sparkwing.Plan` | `sparkwing/plan.go` | Plan holds the job graph populated by a pipeline's Plan method. |
| `sparkwing.NewPlan` | `sparkwing/plan.go` | Creates an empty job declaration graph. |
| `sparkwing.Job` | `sparkwing/plan.go` | Job registers a Workable or func(context.Context) error under id. |
| `sparkwing.JobNode` | `sparkwing/plan.go` | JobNode is a Plan entry wrapping a Workable. |
| `sparkwing.ApprovalTimeoutPolicy` | `sparkwing/plan.go` | ApprovalTimeoutPolicy enumerates the resolution applied to an unanswered approval gate when its Timeout elapses. |
| `sparkwing.SkipPredicate` | `sparkwing/plan.go` | SkipPredicate is a function evaluated after upstream dependencies complete. |
| `sparkwing.BeforeRunFn` | `sparkwing/plan.go` | BeforeRunFn runs once before the first Run attempt. |
| `sparkwing.AfterRunFn` | `sparkwing/plan.go` | AfterRunFn receives the final Run error after all retries, or nil on success. |
| `sparkwing.Dep` | `sparkwing/plan.go` | Accepts job, approval and job-group handles as prerequisites. |
| `sparkwing.JobNode.ID` | `sparkwing/plan.go` | Returns the declared job identifier. |
| `sparkwing.JobNode.Needs` | `sparkwing/plan.go` | Needs declares hard upstream dependencies. |
| `sparkwing.RetryConfig` | `sparkwing/plan.go` | RetryConfig is the resolved retry envelope for a Job. |
| `sparkwing.RetryOption` | `sparkwing/plan.go` | RetryOption tunes a Retry(...) call. |
| `sparkwing.JobNode.Retry` | `sparkwing/plan.go` | Retry configures the job to be re-attempted up to attempts additional times on failure. |
| `sparkwing.JobNode.Timeout` | `sparkwing/plan.go` | Timeout caps the per-attempt duration. |
| `sparkwing.JobNode.NoProgressTimeout` | `sparkwing/plan.go` | NoProgressTimeout caps how long an attempt may run without emitting a job log record. |
| `sparkwing.JobNode.OnFailure` | `sparkwing/plan.go` | OnFailure declares and returns a recovery job that runs when this job fails. |
| `sparkwing.SkipOption` | `sparkwing/plan.go` | SkipOption configures a SkipIf registration. |
| `sparkwing.SkipBudget` | `sparkwing/plan.go` | SkipBudget sets the timeout applied to each predicate on the job. |
| `sparkwing.JobNode.SkipIf` | `sparkwing/plan.go` | SkipIf registers a predicate evaluated on the executing host after this node's dependencies complete and its concurrency groups are acquired. |
| `sparkwing.JobNode.BeforeRun` | `sparkwing/plan.go` | BeforeRun registers a hook to run once before the job's Run method on the first attempt. |
| `sparkwing.JobNode.AfterRun` | `sparkwing/plan.go` | AfterRun registers a hook invoked after Run and all retries finish. |
| `sparkwing.JobNode.Verify` | `sparkwing/plan.go` | Verify checks a postcondition after each successful action attempt. |
| `sparkwing.JobNode.Requires` | `sparkwing/plan.go` | Requires records label terms used to filter runner claims for non-inline dispatched jobs. |
| `sparkwing.JobNode.Prefers` | `sparkwing/plan.go` | Declares soft runner-label preferences for host matching. |
| `sparkwing.JobNode.Inline` | `sparkwing/plan.go` | Requests execution on the run host. |
| `sparkwing.JobNode.ContinueOnError` | `sparkwing/plan.go` | ContinueOnError tells the host that downstream dependents should proceed even when this job fails. |
| `sparkwing.JobNode.Optional` | `sparkwing/plan.go` | Optional marks the job as non-essential: a failure is logged as a warning and does not count toward the run's overall success/fail outcome. |
| `sparkwing.Plan.Priority` | `sparkwing/priority.go` | Declares run admission priority for host resolution. |
| `sparkwing.Produces` | `sparkwing/produces.go` | Produces is a zero-size marker embedded in a Job to declare its output contract at the type level. |
| `sparkwing.Ref` | `sparkwing/ref.go` | Ref is a typed reference to another job's output. |
| `sparkwing.Ref.Job` | `sparkwing/ref.go` | Job returns the upstream job id this reference points at. |
| `sparkwing.Ref.Get` | `sparkwing/ref.go` | Get resolves the reference to a typed T value. |
| `sparkwing.Ref.TryGet` | `sparkwing/ref.go` | TryGet resolves the reference like Get but reports an absent upstream output instead of panicking. |
| `sparkwing.RefTo` | `sparkwing/ref.go` | RefTo returns a RefT pointing at an in-run job's output. |
| `sparkwing.RefOption` | `sparkwing/ref.go` | RefOption tunes a cross-pipeline ref constructor (RefToLastRun). |
| `sparkwing.MaxAge` | `sparkwing/ref.go` | MaxAge bounds cross-pipeline ref resolution to runs whose finished_at is within d. |
| `sparkwing.RefToLastRun` | `sparkwing/ref.go` | References a job output in the pipeline's newest successful run. |
| `sparkwing.RunnerInfo` | `sparkwing/runner.go` | RunnerInfo describes the runner that's about to execute (or is executing) the current job. |
| `sparkwing.RunnerInfo.HasLabel` | `sparkwing/runner.go` | HasLabel reports whether the runner advertises the given label term. |
| `sparkwing.Runner` | `sparkwing/runner.go` | Reads the granted runner identity delivered by hello. |
| `sparkwing.Secret` | `sparkwing/secret.go` | Secret resolves a masked value through the resolver installed on ctx. |
| `sparkwing.MustSecret` | `sparkwing/secret.go` | MustSecret is Secret that panics on error. |
| `sparkwing.SecretsProvider` | `sparkwing/secrets.go` | SecretsProvider is optionally implemented by a pipeline value to declare its typed secrets struct. |
| `sparkwing.PipelineSecrets` | `sparkwing/secrets.go` | PipelineSecrets returns the typed Secrets struct installed on ctx, or nil when the pipeline doesn't implement SecretsProvider. |
| `services.Service` | `sparkwing/services/services.go` | Service describes a sidecar container started with docker run -d. |
| `services.WithServices` | `sparkwing/services/services.go` | WithServices starts every given Service, waits for each to become ready, invokes fn, and then tears the services down. |
| `services.Addr` | `sparkwing/services/services.go` | Addr is where one service ended up, in the order the services were given. |
| `services.WithServicesAddrs` | `sparkwing/services/services.go` | WithServicesAddrs is WithServices that also hands fn one Addr per service, in the order given, which is where an AutoPort port is reported. |
| `sparkwing.JobNode.NeedsTools` | `sparkwing/tools.go` | Requires runner tool labels without installing tools. |
| `sparkwing.Plan.ClaimWait` | `sparkwing/tools.go` | Declares the maximum ready-job wait for a runner claim. |
| `sparkwing.ConcurrencyGroup.String` | `sparkwing/toolslot.go` | Formats the group name and budget for diagnostics. |
| `sparkwing.FailureStage` | `sparkwing/verify.go` | FailureStage identifies which lifecycle stage produced a job failure. |
| `sparkwing.FailureStage.String` | `sparkwing/verify.go` | String returns "action" or "verify". |
| `sparkwing.Failure` | `sparkwing/verify.go` | Failure describes why a job terminated unsuccessfully. |
| `sparkwing.VerifyFn` | `sparkwing/verify.go` | VerifyFn is a postcondition checked after a job's action succeeds. |
| `sparkwing.FailureRecoveryFn` | `sparkwing/verify.go` | FailureRecoveryFn is the failure-aware recovery shape JobNode.OnFailure accepts in addition to a Workable or a func(ctx) error. |
| `sparkwing.VerifyError` | `sparkwing/verify.go` | VerifyError wraps the error returned by a job's Verify check. |
| `sparkwing.VerifyError.Error` | `sparkwing/verify.go` | Formats the error for diagnostics. |
| `sparkwing.VerifyError.Unwrap` | `sparkwing/verify.go` | Exposes the underlying failure to errors.Is and errors.As. |
| `sparkwing.WithArgs` | `sparkwing/withargs.go` | WithArgs is the embedded helper a job uses to declare typed args. |
| `sparkwing.WithArgs.Args` | `sparkwing/withargs.go` | Args returns the resolved typed args for the current run. |
| `sparkwing.Workable` | `sparkwing/work.go` | Workable is the interface every dispatchable Job satisfies: a struct that exposes its inner DAG via Work(w). |
| `sparkwing.Work` | `sparkwing/work.go` | Work is the inner DAG of a Job. |
| `sparkwing.ParallelFailurePolicy` | `sparkwing/work.go` | ParallelFailurePolicy controls whether independent Work items finish after one of their siblings fails. |
| `sparkwing.NewWork` | `sparkwing/work.go` | Creates an empty step declaration graph. |
| `sparkwing.Step` | `sparkwing/work.go` | Step registers a unit of work on this Work. |
| `sparkwing.StepGet` | `sparkwing/work.go` | StepGet blocks until step has completed, then returns its typed output as T. |
| `sparkwing.WorkStep` | `sparkwing/work.go` | WorkStep is one unit of work inside a Work. |
| `sparkwing.WorkStep.ID` | `sparkwing/work.go` | ID returns the step's identifier. |
| `sparkwing.WorkDep` | `sparkwing/work.go` | WorkDep is the closed type set accepted by Work-layer WorkStep.Needs and StepGroup.Needs. |
| `sparkwing.WorkStep.Needs` | `sparkwing/work.go` | Needs declares hard upstream Step dependencies inside the same Work. |
| `sparkwing.WorkStep.SkipIf` | `sparkwing/work.go` | SkipIf registers a predicate the runner evaluates after this step's upstream deps complete. |
| `sparkwing.WorkStep.ContinueOnError` | `sparkwing/work.go` | ContinueOnError marks the step's failure as non-blocking for the rest of the Work: in-flight sibling steps are not cancelled, and downstream steps that .Needs() this one still dispatch. |
| `sparkwing.WorkStep.Optional` | `sparkwing/work.go` | Optional marks the step as non-essential: a failure is recorded (still visible in logs and step status) but does not count toward the Job's rollup outcome. |
| `sparkwing.WorkStep.Finally` | `sparkwing/work.go` | Finally marks a step as cleanup. |
| `sparkwing.StepGroup` | `sparkwing/work.go` | StepGroup is a handle to a named group of Steps. |
| `sparkwing.StepGroup.Name` | `sparkwing/work.go` | Name returns the group's declared name. |
| `sparkwing.StepGroup.Members` | `sparkwing/work.go` | Members returns the group's steps. |
| `sparkwing.GroupSteps` | `sparkwing/work.go` | GroupSteps declares a named bundle of Work steps. |
| `sparkwing.StepGroup.Needs` | `sparkwing/work.go` | Needs declares an upstream dependency on every member of the group. |
| `sparkwing.StepGroup.SkipIf` | `sparkwing/work.go` | SkipIf registers a predicate on every member of the group. |
| `sparkwing.StepError` | `sparkwing/work_run.go` | StepError wraps a step body's error with the originating step ID. |
| `sparkwing.StepError.Error` | `sparkwing/work_run.go` | Formats the error for diagnostics. |
| `sparkwing.StepError.Unwrap` | `sparkwing/work_run.go` | Exposes the underlying failure to errors.Is and errors.As. |

### Delete

| Symbol | Source | Purpose |
|---|---|---|
| `sparkwing.Plan.AdmissionClassValue` | `sparkwing/admission.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Admission` | `sparkwing/admission.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Admitted` | `sparkwing/admission.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.Outputs` | `sparkwing/artifacts.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RiskBlockedError` | `sparkwing/blast_radius.go` | Host-only risk enforcement. |
| `sparkwing.RiskBlockedError.Error` | `sparkwing/blast_radius.go` | Host-only risk enforcement. |
| `sparkwing.JobNode.OutputGlobs` | `sparkwing/artifacts.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ConsumeEdge` | `sparkwing/artifacts.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.ConsumeEdges` | `sparkwing/artifacts.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.Risks` | `sparkwing/blast_radius.go` | Remove the old API during SDK decoupling. |
| `sparkwing.MemoizeConfig` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.MemoizeOption` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.TTL` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.Memoize` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.MemoizeConfig` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.Memoize` | `sparkwing/cache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.CacheKey.IsNoCache` | `sparkwing/cachekey.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Key` | `sparkwing/cachekey.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.CheckoutValue` | `sparkwing/checkout.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.Dynamic` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.Ready` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.Err` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobFanOutDynamic` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.Outputs` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.WhenRunner` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.EnvOld` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.CacheDir` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobGroup.NeedsOptional` | `sparkwing/combinator.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.ConcurrencyGroupRef` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.ConcurrencyCost` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PlanConcurrency` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.PlanConcurrency` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.ConcurrencyGroupRef` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.ConcurrencyCost` | `sparkwing/concurrency.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PullRequestFromEnv` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ResourceSample` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ResourceReporter` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithResourceReporter` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.LoggerFromContext` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.NodeFromContext` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithStep` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.StepFromContext` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.AnnotateOld` | `sparkwing/context.go` | Remove the old API during SDK decoupling. |
| `sparkwing.DebugEnabled` | `sparkwing/debug.go` | Remove the old API during SDK decoupling. |
| `sparkwing.NpmCache` | `sparkwing/depcache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.CacheDir` | `sparkwing/depcache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.DirCaches` | `sparkwing/depcache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.DescribePipeline` | `sparkwing/describe.go` | Remove the old API during SDK decoupling. |
| `sparkwing.DescribeStepRisks` | `sparkwing/describe.go` | Remove the old API during SDK decoupling. |
| `sparkwing.DescribeArg` | `sparkwing/describe.go` | Remove the old API during SDK decoupling. |
| `sparkwing.DescribeArg.CheckEnum` | `sparkwing/describe.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.HasDryRun` | `sparkwing/dry_run.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.IsSafeWithoutDryRun` | `sparkwing/dry_run.go` | Remove the old API during SDK decoupling. |
| `inputs.RepoFilesOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `inputs.FilesOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `inputs.TreeOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `inputs.EnvOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `inputs.ConstOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `inputs.ComposeOld` | `sparkwing/inputs/inputs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.InputSchema` | `sparkwing/inputs_schema.go` | Remove the old API during SDK decoupling. |
| `sparkwing.InputField` | `sparkwing/inputs_schema.go` | Remove the old API during SDK decoupling. |
| `sparkwing.InputField.IsExtraBag` | `sparkwing/inputs_schema.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SecretField` | `sparkwing/inspect.go` | Remove the old API during SDK decoupling. |
| `sparkwing.InspectPipelineSecrets` | `sparkwing/inspect.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.JobArgs` | `sparkwing/jobargs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SkipArgResolve` | `sparkwing/jobargs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.OIDCTokenSource` | `sparkwing/oidc.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Registration` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.BindPipelinesFromYAML` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Lookup` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Registration.SecretValues` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Registration.SecretArgNames` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Registration.Instance` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Registered` | `sparkwing/pipeline.go` | Remove the old API during SDK decoupling. |
| `sparkwing.AwaitRequest` | `sparkwing/pipeline_await.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PipelineAwaiter` | `sparkwing/pipeline_await.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PipelineAwaiterFunc` | `sparkwing/pipeline_await.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PipelineAwaiterFunc.Await` | `sparkwing/pipeline_await.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ResolvedPipelineRef` | `sparkwing/pipeline_ref.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PipelineResolver` | `sparkwing/pipeline_ref.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PipelineResolverFunc` | `sparkwing/pipeline_ref.go` | Remove the old API during SDK decoupling. |
| `sparkwing.LintWarning` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.Inputs` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.Nodes` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.Job` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.Expansions` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ExpandGenerator` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Expansion` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.LintWarnings` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalConfigOld` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.IsApproval` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.ApprovalConfigOld` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobApproval` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.Job` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.ID` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.Needs` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.NeedsOptional` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.OnFailure` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.BeforeRun` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.AfterRun` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.SkipIf` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.Optional` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ApprovalGate.ContinueOnError` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.CacheKey` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.CacheKeyFn` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.Job` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.Work` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.ResultStep` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.NeedsGroups` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.DepIDs` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.EnvOld` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.EnvMap` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.OutputType` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RetryBackoff` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RetryAuto` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.RetryConfig` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.TimeoutDuration` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.NoProgressTimeoutDuration` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.OnFailureNode` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.SkipPredicates` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.SkipIfBudget` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.Verifier` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.BeforeRunHooks` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.AfterRunHooks` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.RequiresLabels` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.PrefersLabels` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.WhenRunner` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.WhenRunnerLabels` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.IsInline` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.JobGroupNames` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.GroupSourceIDs` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.IsDynamicNode` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.IsContinueOnError` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.IsOptional` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.NeedsOptional` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.OptionalDepIDs` | `sparkwing/plan.go` | Remove the old API during SDK decoupling. |
| `planguard.With` | `sparkwing/planguard/planguard.go` | Remove the old API during SDK decoupling. |
| `planguard.Active` | `sparkwing/planguard/planguard.go` | Remove the old API during SDK decoupling. |
| `planguard.Guard` | `sparkwing/planguard/planguard.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.PriorityValue` | `sparkwing/priority.go` | Remove the old API during SDK decoupling. |
| `sparkwing.JobNode.PipelineRefs` | `sparkwing/ref.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RefTarget` | `sparkwing/ref.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RuntimeConfig` | `sparkwing/runtime_config.go` | Remove the old API during SDK decoupling. |
| `sparkwing.CurrentRuntime` | `sparkwing/runtime_config.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SetWorkDir` | `sparkwing/runtime_config.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SetGit` | `sparkwing/runtime_config.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SecretResolver` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SecretResolverFunc` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SecretResolverFunc.Resolve` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithSecretResolver` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Config` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.MustConfig` | `sparkwing/secret.go` | Remove the old API during SDK decoupling. |
| `sparkwing.NewSecretResolverFromSpec` | `sparkwing/source_resolver.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SparkwingFlagDoc` | `sparkwing/sparkwing_flag_docs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SparkwingFlagDocs` | `sparkwing/sparkwing_flag_docs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.PreviewSkipForRange` | `sparkwing/step_range.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.TopologicalStepOrder` | `sparkwing/step_range.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ToolCacheDir` | `sparkwing/toolcache.go` | Remove the old API during SDK decoupling. |
| `sparkwing.LintCacheBlobKey` | `sparkwing/toolcache_blobstore.go` | Remove the old API during SDK decoupling. |
| `sparkwing.SaveLintCache` | `sparkwing/toolcache_blobstore.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RestoreLintCache` | `sparkwing/toolcache_blobstore.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Plan.ClaimWaitValue` | `sparkwing/tools.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ToolSlotProvider` | `sparkwing/toolslot.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithToolSlotProvider` | `sparkwing/toolslot.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ToolSlot` | `sparkwing/toolslot.go` | Remove the old API during SDK decoupling. |
| `sparkwing.BoxToolBudget` | `sparkwing/toolslot.go` | Remove the old API during SDK decoupling. |
| `sparkwing.ToolCostCenticores` | `sparkwing/toolslot.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithFailure` | `sparkwing/verify.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithArgs.BindFromAny` | `sparkwing/withargs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WithArgs.ArgsType` | `sparkwing/withargs.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.ParallelFailures` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.ParallelFailurePolicy` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.Steps` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.StepByID` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.Work.Groups` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.OutputType` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.DepIDs` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.SkipPredicates` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.IsContinueOnError` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.IsOptional` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.IsFinally` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WorkStep.Output` | `sparkwing/work.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RunWork` | `sparkwing/work_run.go` | Remove the old API during SDK decoupling. |
| `sparkwing.RequiresProvider` | `sparkwing/workable_labels.go` | Remove the old API during SDK decoupling. |
| `sparkwing.PrefersProvider` | `sparkwing/workable_labels.go` | Remove the old API during SDK decoupling. |
| `sparkwing.WhenRunnerProvider` | `sparkwing/workable_labels.go` | Remove the old API during SDK decoupling. |

## Fake-host method inventory

| Method | Purpose |
|---|---|
| `Host.Context` | Supplies a context whose host services resolve from this fixture. |
| `Host.Plan` | Invokes a registered planner with typed argument resolution and returns the strict wire plan. |
| `Host.RunStep` | Executes one declared job step and returns its encoded typed output. |
| `Host.Eval` | Evaluates an author's job closure using public SDK types and JSON input/output. |
| `Host.Records` | Returns a copy of structured records emitted through the fixture. |
| `Host.Masks` | Returns a copy of values whose registration has been acknowledged. |
| `Host.Close` | Cancels pending work and releases fixture resources. |

## Retained constants

- `ScopeRun`, `ScopeBox`, `ScopeGlobal` declare run, machine, and shared concurrency scope. Box scope is job-only. Pipeline acceptance refuses a box-scoped plan group with an error naming the group and pointing at job-level box scope. Plan and job groups have separate namespaces.
- `Queue`, `Fail`, `Skip`, `CancelOthers` select capacity policy.
- `ApprovalFail`, `ApprovalDeny`, `ApprovalApprove` select approval timeout resolution.
- `FailFast`, `CollectAll` select independent-step failure policy.
- `StageAction`, `StageVerify` distinguish body and verification failure.
- `Success`, `Failed`, `Cancelled`, `Skipped`, `Superseded` retain the final outcome values. `Memoized` is added in the contract file.
- `ErrRefAbsent` retains detectable absence for typed references. Host transport errors never become absence.

`NoMemo` is the explicit reuse bypass. Every other engine event, environment-key, or old outcome constant remains implementation-only or is removed with the old runtime.

`CheckoutOld` and `Plan.CheckoutOld` retain only the old runtime and fixtures. Both are deleted during implementation.

Runner label declarations pass through both SDKs unchanged. Host plan acceptance
and settings resolution share one normalizer that trims outer whitespace, removes
empty labels and keeps the first occurrence in declaration order. Selector
alternatives retain their OR semantics. The SDKs neither normalize nor reject
empty runner labels.

Runner sizing uses the existing `Requires` declarations. The SDK exposes no CPU or memory request API. Size labels use the normal selector term semantics and Cloud maps its fixed catalog labels to physical allocations and exact credit classes.
