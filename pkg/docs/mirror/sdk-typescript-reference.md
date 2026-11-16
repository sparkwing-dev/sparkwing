<!-- GENERATED from sdk/typescript/index.d.ts by sdk/typescript/tools/reference.ts. Do not edit by hand; regenerate with `bash bin/gen-ts-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# TypeScript SDK API reference

Every export of `@sparkwing/sdk`, generated from the package's `index.d.ts`, which is also what your editor reads from `node_modules/@sparkwing/sdk`. For concepts and examples in Go and TypeScript side by side, see [sdk.md](sdk.md). The Go package has its own reference, [sdk-reference.md](sdk-reference.md).

## Functions

- `function compose(...inputs: readonly MemoInput[]): MemoInput` -- Combines input recipes in order.
- `function constant(value: string): MemoInput` -- Declares a nonempty literal string, validated by the host.
- `function definePipeline(definition: PipelineDef): PipelineDef` -- Registration exposes one entrypoint for host-side YAML alias resolution.
- `function dir(path: string, ...keyFiles: readonly string[]): CacheSpec` -- Custom dependency files contribute to the directory cache key.
- `function env(...names: readonly string[]): MemoInput` -- The effective job environment distinguishes unset from empty.
- `function files(...globs: readonly string[]): MemoInput` -- Matching tracked files contribute to host-computed memo identity.
- `function gitFromTree(ctx: Context, workDir: string): Promise<Git>` -- Explicit tree discovery supports author-owned sibling checkouts.
- `function githubOwnerRepo(slug: string): { owner: string; repo: string }` -- Slug parsing keeps owner/repo handling identical across helper packages.
- `function goModules(): CacheSpec` -- Go cache directories restore through the common depcache path.
- `function ignore(...patterns: readonly string[]): RepoFilesOption` -- Basenames match anywhere, trailing slashes match a root prefix, and ** spans segments.
- `function main(): Promise<void>` -- Main negotiates hello before handling any operation or opening bound routes.
- `function memoKey(...parts: readonly unknown[]): readonly unknown[] | typeof noMemo` -- Collects ordered JSON parts for host hashing.
- `function npm(): CacheSpec` -- npm cache directories restore through the common depcache path.
- `function parts(...values: readonly unknown[]): MemoInput` -- Declares ordered JSON values for host hashing.
- `function refTo<T>(job: Job<T>): Ref<T>` -- A job output reference retains its declared result type.
- `function refToLastRun<T>(pipeline: string, jobId: string, options?: RefOptions): PipelineRef<T>` -- Declarative references let the host authorize cross-pipeline reads.
- `function repoFiles(...options: readonly RepoFilesOption[]): MemoInput` -- Repository-relative selection includes all tracked files except declared exclusions.
- `function startup(): EnvOption` -- Startup values must not influence declarations; drift fails the attempt.
- `function tree(path: string): MemoInput` -- Regular files contribute recursively inside the checkout, skipping symlinks; missing roots fail on the host.
- `function withCommandEnv(ctx: Context, env: Readonly<Record<string, string>>): Context` -- Context-level command env is inherited until explicit command options override it.
- `function workDir(): string` -- Root-relative paths match command cwd and file helper resolution.

## Classes

### ConcurrencyGroup

`class ConcurrencyGroup`

One declaration lets all members share the same capacity policy.

- `constructor(name: string, limit?: ConcurrencyLimit)`
- `readonly name: string`
- `readonly limit: Readonly<ConcurrencyLimit>`
- `toString(): string`

### ExecError

`class ExecError extends Error`

Command failure preserves output and termination cause for diagnostics.

- `constructor(result: ExecResult, terminated?: "" | "cancellation" | "signal", cause?: unknown)`
- `readonly command: string`
- `readonly stdout: string`
- `readonly stderr: string`
- `readonly exitCode: number`
- `readonly cause?: unknown`
- `readonly terminated: "" | "cancellation" | "signal"`

### Git

`class Git implements GitInfo`

Git snapshots stay sealed while live methods inspect the current working tree.

- `constructor(workDir: string, facts: GitInfo)`
- `readonly repo: string`
- `readonly branch: string`
- `readonly commit: string`
- `readonly defaultBranch?: string`
- `readonly repoURL?: string`
- `workDir(): string`
- `shortSHA(): string`
- `name(): string`
- `isDirty(ctx: Context): Promise<boolean>`
- `filesetHash(ctx: Context): Promise<string>`
- `changedFiles(ctx: Context, since: string): Promise<string[]>`
- `tagsAtHead(ctx: Context): Promise<string[]>`
- `latestTag(ctx: Context, prefix: string): Promise<string>`
- `pushTag(ctx: Context, tag: string, message: string): Promise<void>`

### InputError

`class InputError extends Error`

Input errors distinguish invalid author arguments from execution failures.

- `constructor(cause: unknown)`
- `readonly cause: unknown`

### Job

`class Job<T = unknown>`

Jobs declare execution policy; hosts own placement, validation and persistence.

- `readonly id: string`
- `needs(...jobs: readonly Dependency[]): this`
- `needsIfPresent(...jobs: readonly Dependency[]): this`
- `needsGenerated(...generators: readonly Generator<unknown>[]): this`
- `references(...refs: readonly PipelineRef<unknown>[]): this`
- `withArgs(...args: readonly ArgSpec[]): this`
- `startsChildren(...pipelines: readonly string[]): this` -- Declare child pipelines in the parent repository. Calls append targets.
- `startsChildrenIn(repository: string, ...pipelines: readonly string[]): this` -- Declare child pipelines using a canonical repository key, such as gh:123.
- `step<U = unknown>(id: string, body: Body<U>): Step<U>`
- `groupSteps(id: string, ...steps: readonly Step<unknown>[]): StepGroup`
- `failurePolicy(policy: FailurePolicy): this`
- `retry(attempts: number, options?: RetryOptions): this`
- `timeout(ms: number): this`
- `noProgressTimeout(ms: number): this`
- `cores(n: number): this`
- `skipIf(predicate: Predicate, options?: SkipOptions): this`
- `beforeRun(hook: Hook): this`
- `verify(hook: Hook): this`
- `afterRun(hook: AfterRunFn): this`
- `memo(key: MemoInput | MemoKeyFn, options?: MemoOptions): this`
- `cache(...caches: readonly CacheSpec[]): this`
- `artifacts(...globs: readonly string[]): this`
- `consumes(producer: Dependency, options?: { into?: string }): this`
- `onFailure<U = unknown>(id: string, body: Body<U>): Job<U>`
- `concurrency(group: ConcurrencyGroup, cost?: number): this`
- `needsTools(...names: readonly string[]): this`
- `requires(...labels: readonly string[]): this`
- `prefers(...labels: readonly string[]): this`
- `inline(): this`
- `optional(): this`
- `continueOnError(): this`
- `env(name: string, value: string, option?: EnvOption): this`
- `generate(fn: GenerateFn<T>): Generator<T>`

### NoProjectError

`class NoProjectError extends Error`

Missing project roots are distinguishable from ordinary filesystem errors.

### Plan

`class Plan`

Plan knobs are SDK-owned inputs resolved at the second seal point.

- `constructor()`
- `job<T = unknown>(id: string, body?: Body<T>): Job<T>`
- `approval(id: string, options?: ApprovalOptions): Approval`
- `groupJobs(id: string, ...jobs: readonly Job<unknown>[]): JobGroup`
- `jobFanOut<I, T = unknown>(id: string, items: readonly I[], fn: (item: I, index: number) => readonly [string, Body<T>]): JobGroup` -- One job per item, grouped under id; an empty list gives a group with no members.
- `concurrency(group: ConcurrencyGroup, cost?: number): this`
- `priority(value: number): this`
- `claimWait(ms: number): this`
- `admissionClass(value: string): this`
- `checkout(options: Checkout): this`

### SDKError

`class SDKError extends Error`

Callers branch on code; unknown codes remain ordinary failures.

- `constructor(message: string, options?: { code: string; hint?: string; details?: Readonly<Record<string, unknown>> })`
- `readonly code: string`
- `readonly hint?: string`
- `readonly details?: Readonly<Record<string, unknown>>`

### Step

`class Step<T = unknown>`

Step handles retain output type across dependency reads.

- `readonly id: string`
- `needs(...steps: readonly (Step<unknown> | StepGroup | string)[]): this`
- `result(): this`
- `skipIf(predicate: Predicate): this`
- `finally(): this`
- `continueOnError(): this`
- `optional(): this`
- `dryRun(body: Body<T>): this`
- `safeWithoutDryRun(): this`
- `risk(...labels: readonly string[]): this` -- Every host enforces risk labels before real work.

### StepError

`class StepError extends Error`

Step errors preserve the failing step and its underlying failure.

- `constructor(stepId: string, cause: unknown)`
- `readonly stepId: string`
- `readonly cause: unknown`

### VerifyError

`class VerifyError extends Error`

Verification errors preserve postcondition failure classification.

- `constructor(cause: unknown)`
- `readonly cause: unknown`

## Interfaces

### Annotation

`interface Annotation`

File locations let consumers link a diagnostic to source.

- `message: string`
- `level?: Level`
- `file?: string`
- `line?: number`
- `endLine?: number`
- `column?: number`

### Approval

`interface Approval`

Approvals cannot declare concurrency groups; put them on the job after the approval. skipIf runs on the run's coordinator before requesting approval, holding no capacity, and true settles as skipped.

- `readonly id: string`
- `skipIf(predicate: Predicate, options?: SkipOptions): this`
- `needs(...jobs: readonly Dependency[]): this`
- `needsIfPresent(...jobs: readonly Dependency[]): this`
- `needsGenerated(...generators: readonly Generator<unknown>[]): this`
- `optional(): this`
- `continueOnError(): this`
- `onFailure<T = unknown>(id: string, body: Body<T>): Job<T>`

### ApprovalOptions

`interface ApprovalOptions`

Approval zero timeout preserves indefinite waits where the owner can wait.

- `message?: string`
- `timeoutMs?: number`
- `onTimeout?: "fail" | "deny" | "approve"`

### ArgSpec

`interface ArgSpec`

Omission preserves precedence; explicit zero never means unset.

- `name: string`
- `type: "string" | "bool" | "int" | "int64" | "float64" | "duration" | "[]string"`
- `description?: string`
- `short?: string`
- `default?: string`
- `required?: boolean`
- `secret?: boolean`
- `enum?: readonly string[]`

### CacheSpec

`interface CacheSpec`

Opaque cache descriptors prevent authors from selecting privileged ref scopes.

- `readonly kind: "go_modules" | "npm" | "dir"`
- `readonly path?: string`
- `readonly keyFiles?: readonly string[]`

### Checkout

`interface Checkout`

Checkout omission inherits source policy; fullHistory requests all history explicitly.

- `depth?: number`
- `fullHistory?: boolean`
- `tags?: boolean`
- `submodules?: boolean`
- `lfs?: boolean`

### ChildOptions

`interface ChildOptions<In extends object = Record<string, string | number | boolean | readonly string[]>>`

Child inputs are explicit and are not inherited from the parent.

- `args?: Args`
- `inputs?: { readonly [K in keyof In]: In[K] extends string | number | boolean | readonly string[] ? In[K] : never }` -- Input property names are argument names; explicit args override overlapping inputs.
- `repo?: string` -- Omission inherits parent repo/SHA; an explicit repo selects fresh branch-tip source.
- `branch?: string` -- Omission uses main for an explicit repo, otherwise the parent branch.
- `timeoutMs?: number` -- Includes queue time; expiry stops waiting without cancelling the child.

### CleanupRegistration

`interface CleanupRegistration`

Disarming removes a recorded cleanup after the resource was released.

- `readonly id: string`
- `unregister(): Promise<void>`

### CleanupSpec

`interface CleanupSpec`

Cleanup survives SDK process loss because the host records argv first.

- `argv: readonly [string, ...string[]]`
- `description?: string`

### Cmd

`interface Cmd`

A command executes only when a result method is called.

- `dir(path: string): this`
- `env(name: string, value: string): this`
- `envMap(values: Readonly<Record<string, string>>): this`
- `run(): Promise<ExecResult>` -- Logs output; when the host runs the command, the result keeps the last 64 KiB of each stream.
- `capture(): Promise<ExecResult>`
- `string(): Promise<string>`
- `lines(): Promise<string[]>`
- `json<T = unknown>(): Promise<T>`
- `mustBeEmpty(reason: string): Promise<void>`

### ConcurrencyLimit

`interface ConcurrencyLimit`

Optional fields distinguish inherited policy from explicit values.

- `capacity?: number`
- `scope?: Scope`
- `onLimit?: OnLimit`
- `queueTimeoutMs?: number`

### Context

`interface Context`

Bound operations use the hello bearer in memory and never expose it here.

- `readonly run: RunContext`
- `readonly jobId: string`
- `readonly stepId?: string`
- `readonly args: Args`
- `readonly dryRun: boolean`
- `readonly runner: RunnerInfo`
- `readonly failure?: Failure`
- `readonly signal: AbortSignal`
- `readonly log: Logger`
- `secret(name: string): Promise<string | undefined>` -- Undeclared reads fail; optional missing values return undefined.
- `variable(name: string): Promise<string | undefined>`
- `output<T>(job: Job<T> | string): Promise<T | undefined>`
- `stepOutput<T>(step: Step<T> | string): T | undefined`
- `annotate(annotation: Annotation): Promise<void>`
- `summary(markdown: string): Promise<void>`
- `mask(value: string): Promise<void>` -- Resolution means the host registered the mask before subsequent logs.
- `oidcToken(audience: string): Promise<string>`
- `runPipeline<T, In extends object = Record<string, string | number | boolean | readonly string[]>>(pipeline: string, jobId: string, options?: ChildOptions<In>): Promise<T>`
- `latest<T>(reference: PipelineRef<T>): Promise<T>`
- `cleanup(spec: CleanupSpec): Promise<CleanupRegistration>`
- `exec(argv: readonly [string, ...string[]], options?: ExecOptions): Cmd`
- `bash(script: string, options?: ExecOptions): Cmd`

### EnvironmentDescription

`interface EnvironmentDescription`

Environment metadata documents author inputs, never transport credentials.

- `name: string`
- `description?: string`
- `default?: string`

### EnvOption

`interface EnvOption`

Startup env is applied before interpreter initialization.

- `readonly phase: "startup"`

### ExecOptions

`interface ExecOptions`

Exec inherits attempt env, including body env, without protocol credentials.

- `cwd?: string`
- `env?: Readonly<Record<string, string>>`

### ExecResult

`interface ExecResult`

Command output and usage events share one execution helper.

- `readonly command: string`
- `readonly stdout: string`
- `readonly stderr: string`
- `readonly exitCode: number`

### Failure

`interface Failure`

Failure data supports recovery without parsing display text.

- `readonly job: string`
- `readonly stage: string`
- `readonly message: string`

### Generated

`interface Generated`

Generated jobs use the same declaration surface and validator as static jobs.

- `job<T = unknown>(id: string, body?: Body<T>): Job<T>`
- `approval(id: string, options?: ApprovalOptions): Approval`
- `groupJobs(id: string, ...jobs: readonly Job<unknown>[]): JobGroup`
- `jobFanOut<I, T = unknown>(id: string, items: readonly I[], fn: (item: I, index: number) => readonly [string, Body<T>]): JobGroup`

### Generator

`interface Generator<T>`

A generator handle names the settlement barrier consumed by dependencies.

- `readonly id: string`
- `readonly source: Job<T>`

### GitInfo

`interface GitInfo`

Git facts come from the sealed invocation, not a fresh branch lookup.

- `readonly repo: string`
- `readonly branch: string`
- `readonly commit: string`
- `readonly defaultBranch?: string`
- `readonly repoURL?: string`

### JobGroup

`interface JobGroup`

Display grouping can also name a set of job dependencies.

- `readonly id: string`
- `readonly members: readonly Job<unknown>[]`
- `needs(...jobs: readonly Dependency[]): this`
- `needsIfPresent(...jobs: readonly Dependency[]): this`
- `needsGenerated(...generators: readonly Generator<unknown>[]): this`
- `retry(attempts: number, options?: RetryOptions): this`
- `timeout(ms: number): this`
- `noProgressTimeout(ms: number): this`
- `cores(n: number): this`
- `skipIf(predicate: Predicate, options?: SkipOptions): this`
- `beforeRun(hook: Hook): this`
- `verify(hook: Hook): this`
- `afterRun(hook: AfterRunFn): this`
- `memo(key: MemoInput | MemoKeyFn, options?: MemoOptions): this`
- `cache(...caches: readonly CacheSpec[]): this`
- `artifacts(...globs: readonly string[]): this`
- `consumes(producer: Job<unknown>, options?: { into?: string }): this`
- `concurrency(group: ConcurrencyGroup, cost?: number): this`
- `needsTools(...names: readonly string[]): this`
- `requires(...labels: readonly string[]): this`
- `prefers(...labels: readonly string[]): this`
- `inline(): this`
- `optional(): this`
- `continueOnError(): this`
- `env(name: string, value: string, option?: EnvOption): this`

### JobResult

`interface JobResult<T>`

Reasons preserve distinctions that do not warrant another outcome.

- `readonly outcome: Outcome`
- `readonly reason?: string`
- `readonly output?: T`
- `readonly failure?: Failure`

### Logger

`interface Logger`

Structured attributes survive command and closure logging.

- `debug(message: string, attrs?: Record<string, unknown>): void`
- `info(message: string, attrs?: Record<string, unknown>): void`
- `warn(message: string, attrs?: Record<string, unknown>): void`
- `error(message: string, attrs?: Record<string, unknown>): void`

### MemoOptions

`interface MemoOptions`

Memo TTL is ceiling-checked at seal, never clamped.

- `ttlMs?: number`

### PipelineDef

`interface PipelineDef`

Names and secret declarations belong to describe, separate from plan documents.

- `name: string`
- `description?: string`
- `short?: string`
- `help?: string`
- `examples?: readonly UsageExample[]`
- `envVars?: readonly EnvironmentDescription[]`
- `extra?: boolean`
- `args?: readonly ArgSpec[]`
- `secrets?: readonly SecretSpec[]`
- `plan(plan: Plan, run: RunContext): Awaitable<void>`

### PipelineRef

`interface PipelineRef<T> extends Ref<T>`

Latest references read successful verified default-branch runs unless fromAnyRun or fromProvenRunAtCommit is set.

- `readonly pipeline: string`
- `readonly jobId: string`
- `readonly maxAgeMs?: number`
- `readonly fromAnyRun?: boolean`
- `readonly fromProvenRunAtCommit?: boolean`

### PullRequest

`interface PullRequest`

PR base and head facts remain fixed to the triggering event.

- `readonly number: number`
- `readonly action?: string`
- `readonly baseRef?: string`
- `readonly baseSHA?: string`
- `readonly headRef?: string`
- `readonly headSHA?: string`

### Ref

`interface Ref<T>`

Output reads carry an explicit dependency or reference authorization edge.

- `readonly jobId: string`
- `get(ctx: Context): Promise<T>`
- `tryGet(ctx: Context): Promise<T | undefined>` -- Only genuine absence returns undefined; decode, transport and cancellation errors reject.

### RefOptions

`interface RefOptions`

Max age bounds freshness; fromAnyRun permits output without verified default-branch provenance; fromProvenRunAtCommit reads only a run at the reader's commit, proven for its repository, that selected every job without dry run.

- `maxAgeMs?: number`
- `fromAnyRun?: boolean`
- `fromProvenRunAtCommit?: boolean`

### RepoFilesOption

`interface RepoFilesOption`

Excludes matching tracked paths.

- `readonly ignore: readonly string[]`

### RetryOptions

`interface RetryOptions`

App retries retain the same claim and do not spend disruption allowance.

- `backoffMs?: number`

### RunContext

`interface RunContext`

Planning uses the registered entrypoint and the invoked pipeline name separately.

- `readonly id: string`
- `readonly pipeline: string`
- `readonly args: Args`
- `readonly dryRun: boolean`
- `readonly noMemo: boolean`
- `readonly startedAt?: string`
- `readonly runner: RunnerInfo`
- `readonly git: Git`
- `readonly trigger: Trigger`

### RunnerInfo

`interface RunnerInfo`

Identity comes from hello, because process env can disagree with the claim.

- `readonly name: string`
- `readonly type: string`
- `readonly labels: readonly string[]`

### SecretSpec

`interface SecretSpec`

Describe declares names only; values are read lazily in bound processes.

- `name: string`
- `optional?: boolean`
- `variable?: boolean`

### SkipOptions

`interface SkipOptions`

Skip evaluation has a declared budget shared by every placement.

- `budgetMs?: number`

### StepGroup

`interface StepGroup`

Group dependencies expand to all members and modifiers apply to each member.

- `readonly id: string`
- `readonly members: readonly Step<unknown>[]`
- `needs(...steps: readonly (Step<unknown> | StepGroup | string)[]): this`
- `skipIf(predicate: Predicate): this`

### Trigger

`interface Trigger`

Trigger facts remain attached to retries of the same invocation.

- `readonly source: string`
- `readonly user?: string`
- `readonly pullRequest?: PullRequest`

### UsageExample

`interface UsageExample`

Help examples preserve command text and its reason for running.

- `command: string`
- `comment?: string`

## Types

- `type AfterRunFn = (ctx: Context, result: JobResult<unknown>) => Awaitable<void>` -- Final hooks receive the attempt result even when its body failed.
- `type Args = Readonly<Record<string, string>>` -- Args remain serialized strings; []string uses comma splitting, whitespace trimming and empty-item removal.
- `type Awaitable<T> = T | Promise<T>` -- Promise-compatible callbacks allow synchronous declarations and async work.
- `type Body<T = unknown> = (ctx: Context) => Awaitable<T>` -- Bodies return the job or step's JSON-serializable output.
- `type Dependency = Job<unknown> | Approval | JobGroup | string` -- Dependencies refer to declared jobs, approvals, groups or stable IDs.
- `type FailurePolicy = "fail_fast" | "collect_all"` -- Step policy is independent of optional job failure.
- `type GenerateFn<T> = (generated: Generated, output: T, ctx: Context) => Awaitable<void>` -- Generated topology is accepted once; replay only reconstructs code.
- `type Hook = (ctx: Context) => Awaitable<void>` -- Hooks participate in the host's ordered execution layers.
- `type Level = "debug" | "info" | "warn" | "error"` -- Logs and annotations share level spelling.
- `type MemoInput = | { readonly kind: "const"; readonly value: string } | { readonly kind: "files" | "repo_files"; readonly patterns: readonly string[] } | { readonly kind: "tree"; readonly root: string } | { readonly kind: "env"; readonly names: readonly string[] } | { readonly kind: "parts"; readonly parts: readonly unknown[] } | { readonly kind: "compose"; readonly inputs: readonly MemoInput[] }` -- Built-in memo recipes are evaluated by the executing host.
- `type MemoKeyFn = (ctx: Context) => Awaitable<unknown>` -- Author callbacks return JSON values for host hashing, or noMemo to bypass.
- `type OnLimit = "queue" | "fail" | "skip" | "cancel_others"` -- Capacity behavior never implies result reuse.
- `type Outcome = "success" | "failed" | "cancelled" | "skipped" | "superseded" | "memoized"` -- Public outcomes use the same spelling as persisted results.
- `type Predicate = (ctx: Context) => Awaitable<boolean>` -- Predicates are eval closures and cannot alter topology.
- `type Scope = "run" | "box" | "global"` -- Scope is declarative; the host computes the actual coordination key.

## Constants

- `const exitNotStarted: -1` -- Failed launch and signal termination both use -1, distinguished by ExecError.terminated.
- `const noMemo: unique symbol` -- A memo miss sentinel keeps empty keys distinct from disabled reuse.

## Namespaces

### docker

`namespace docker`

Docker helpers retain CLI behavior and host-registered cleanup under DinD.

- `interface BuildConfig { image: string; dockerfile?: string; context?: string; platforms?: readonly string[]; registries?: readonly string[]; tags: readonly string[]; buildArgs?: Readonly<Record<string, string>>; labels?: Readonly<Record<string, string>>; }`
- `interface BuildResult { readonly image: string; readonly digests: Readonly<Record<string, string>>; readonly registries: readonly string[]; }`
- `interface RunOptions { image: string; cmd?: readonly string[]; workDir?: string; env?: Readonly<Record<string, string>>; user?: string; inputDir?: string; outputDir?: string; outputTo?: string; volumes?: Readonly<Record<string, string>>; stdout?: (bytes: Uint8Array) => void; stderr?: (bytes: Uint8Array) => void; }`
- `interface ImageTag { readonly commit: string; readonly content: string; readonly branch: string; readonly dirty: boolean; deployTag(): string; prodTag(): string; all(): string[]; }`
- `class DockerUnavailableError extends Error {}`
- `class BuildxRequiredError extends Error {}`
- `class PlatformUnsupportedError extends Error {}`
- `function build(ctx: Context, config: BuildConfig): Promise<BuildResult>`
- `function buildAndPush(ctx: Context, config: BuildConfig): Promise<BuildResult>`
- `function push(ctx: Context, image: string, tags: readonly string[], registries: readonly string[]): Promise<void>`
- `function login(ctx: Context, registry: string, username: string, secret: string): Promise<void>`
- `function buildxPlatforms(ctx: Context): Promise<string[]>`
- `function filterBuildxPlatforms(ctx: Context, wish: readonly string[]): Promise<string[]>`
- `function run(ctx: Context, options: RunOptions): Promise<void>`
- `function computeTags(ctx: Context): Promise<ImageTag>`
- `function computeTagsIn(ctx: Context, repoDir: string): Promise<ImageTag>`

### git

`namespace git`

Git helpers use promptless direct git and never probe a removed cache service.

- `interface CloneOptions { depth?: number; }`
- `function clone(ctx: Context, url: string, destDir: string, options?: CloneOptions): Promise<void>`
- `function fetch(ctx: Context, repoDir: string): Promise<void>`
- `function shortCommit(ctx: Context, repoDir: string): Promise<string>`
- `function currentSHA(ctx: Context, repoDir: string): Promise<string>`
- `function currentBranch(ctx: Context, repoDir: string): Promise<string>`
- `function defaultBranch(ctx: Context, repoDir: string): Promise<string>`
- `function remoteOriginURL(ctx: Context, repoDir: string): Promise<string>`
- `function isDirty(ctx: Context, repoDir: string): Promise<boolean>`
- `function filesetHash(ctx: Context, repoDir: string): Promise<string>`
- `function changedFiles(ctx: Context, repoDir: string, since: string): Promise<string[]>`
- `function tagsAtHead(ctx: Context, repoDir: string): Promise<string[]>`
- `function latestTag(ctx: Context, repoDir: string, prefix: string): Promise<string>`
- `function tagExistsOnRemote(ctx: Context, repoDir: string, tag: string): Promise<boolean>`
- `function pushTag(ctx: Context, repoDir: string, tag: string, message: string): Promise<void>`

### services

`namespace services`

Service scopes tear down owned containers on success, failure and cancellation.

- `const autoPort: -1`
- `const defaultReadyTimeoutMs: 30000`
- `interface Service { image: string; name?: string; port?: number; hostPort?: number; env?: Readonly<Record<string, string>>; readyCmd?: string; readyTimeoutMs?: number; }`
- `interface Addr { readonly name: string; readonly hostPort: number; }`
- `function withServices(ctx: Context, specs: readonly Service[], fn: (ctx: Context) => Awaitable<void>): Promise<void>`
- `function withServicesAddrs(ctx: Context, specs: readonly Service[], fn: (ctx: Context, addrs: readonly Addr[]) => Awaitable<void>): Promise<void>`
