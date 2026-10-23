/** contract: Promise-compatible callbacks allow synchronous declarations and async work. */
export type Awaitable<T> = T | Promise<T>;
/** contract: Args remain serialized strings; []string uses comma splitting, whitespace trimming and empty-item removal. */
export type Args = Readonly<Record<string, string>>;
/** contract: Omission preserves precedence; explicit zero never means unset. */
export interface ArgSpec { name: string; type: "string" | "bool" | "int" | "int64" | "float64" | "duration" | "[]string"; description?: string; short?: string; default?: string; required?: boolean; secret?: boolean; enum?: readonly string[]; }
/** safety: Describe declares names only; values are read lazily in bound processes. */
export interface SecretSpec { name: string; optional?: boolean; variable?: boolean; }
/** contract: Identity comes from hello, because process env can disagree with the claim. */
export interface RunnerInfo { readonly name: string; readonly type: string; readonly labels: readonly string[]; }
/** contract: Git facts come from the sealed invocation, not a fresh branch lookup. */
export interface GitInfo { readonly repo: string; readonly branch: string; readonly commit: string; readonly defaultBranch?: string; readonly repoURL?: string; }
/** contract: PR base and head facts remain fixed to the triggering event. */
export interface PullRequest { readonly number: number; readonly action?: string; readonly baseRef?: string; readonly baseSHA?: string; readonly headRef?: string; readonly headSHA?: string; }
/** contract: Trigger facts remain attached to retries of the same invocation. */
export interface Trigger { readonly source: string; readonly user?: string; readonly pullRequest?: PullRequest; }
/** contract: Planning uses the registered entrypoint and the invoked pipeline name separately. */
export interface RunContext { readonly id: string; readonly pipeline: string; readonly args: Args; readonly dryRun: boolean; readonly noMemo: boolean; readonly startedAt?: string; readonly runner: RunnerInfo; readonly git: Git; readonly trigger: Trigger; }
/** contract: Failure data supports recovery without parsing display text. */
export interface Failure { readonly job: string; readonly stage: string; readonly message: string; }
/** contract: Public outcomes use the same spelling as persisted results. */
export type Outcome = "success" | "failed" | "cancelled" | "skipped" | "superseded" | "memoized";
/** contract: Reasons preserve distinctions that do not warrant another outcome. */
export interface JobResult<T> { readonly outcome: Outcome; readonly reason?: string; readonly output?: T; readonly failure?: Failure; }
/** contract: Logs and annotations share level spelling. */
export type Level = "debug" | "info" | "warn" | "error";
/** contract: Structured attributes survive command and closure logging. */
export interface Logger { debug(message: string, attrs?: Record<string, unknown>): void; info(message: string, attrs?: Record<string, unknown>): void; warn(message: string, attrs?: Record<string, unknown>): void; error(message: string, attrs?: Record<string, unknown>): void; }
/** contract: File locations let consumers link a diagnostic to source. */
export interface Annotation { message: string; level?: Level; file?: string; line?: number; endLine?: number; column?: number; }
/** safety: Callers branch on code; unknown codes remain ordinary failures. */
export declare class SDKError extends Error { constructor(message: string, options?: { code: string; hint?: string; details?: Readonly<Record<string, unknown>> }); readonly code: string; readonly hint?: string; readonly details?: Readonly<Record<string, unknown>>; }
/** safety: Bound operations use the hello bearer in memory and never expose it here. */
export interface Context {
  readonly run: RunContext;
  readonly jobId: string;
  readonly stepId?: string;
  readonly args: Args;
  readonly dryRun: boolean;
  readonly runner: RunnerInfo;
  readonly failure?: Failure;
  readonly signal: AbortSignal;
  readonly log: Logger;
  /** safety: Undeclared reads fail; optional missing values return undefined. */
  secret(name: string): Promise<string | undefined>;
  variable(name: string): Promise<string | undefined>;
  output<T>(job: Job<T> | string): Promise<T | undefined>;
  stepOutput<T>(step: Step<T> | string): T | undefined;
  annotate(annotation: Annotation): Promise<void>;
  summary(markdown: string): Promise<void>;
  /** safety: Resolution means the host registered the mask before subsequent logs. */
  mask(value: string): Promise<void>;
  oidcToken(audience: string): Promise<string>;
  runPipeline<T, In extends object = Record<string, string | number | boolean | readonly string[]>>(pipeline: string, jobId: string, options?: ChildOptions<In>): Promise<T>;
  latest<T>(reference: PipelineRef<T>): Promise<T>;
  cleanup(spec: CleanupSpec): Promise<CleanupRegistration>;
  exec(argv: readonly [string, ...string[]], options?: ExecOptions): Cmd;
  bash(script: string, options?: ExecOptions): Cmd;
}
/** contract: Bodies return the job or step's JSON-serializable output. */
export type Body<T = unknown> = (ctx: Context) => Awaitable<T>;
/** contract: Predicates are eval closures and cannot alter topology. */
export type Predicate = (ctx: Context) => Awaitable<boolean>;
/** contract: Hooks participate in the host's ordered execution layers. */
export type Hook = (ctx: Context) => Awaitable<void>;
/** contract: Final hooks receive the attempt result even when its body failed. */
export type AfterRunFn = (ctx: Context, result: JobResult<unknown>) => Awaitable<void>;
/** contract: App retries retain the same claim and do not spend disruption allowance. */
export interface RetryOptions { backoffMs?: number; }
/** contract: Skip evaluation has a declared budget shared by every placement. */
export interface SkipOptions { budgetMs?: number; }
/** contract: Memo TTL is ceiling-checked at seal, never clamped. */
export interface MemoOptions { ttlMs?: number; }
/** contract: Scope is declarative; the host computes the actual coordination key. */
export type Scope = "run" | "box" | "global";
/** contract: Capacity behavior never implies result reuse. */
export type OnLimit = "queue" | "fail" | "skip" | "cancel_others";
/** contract: Optional fields distinguish inherited policy from explicit values. */
export interface ConcurrencyLimit { capacity?: number; scope?: Scope; onLimit?: OnLimit; queueTimeoutMs?: number; }
/** contract: One declaration lets all members share the same capacity policy. */
export declare class ConcurrencyGroup { constructor(name: string, limit?: ConcurrencyLimit); readonly name: string; readonly limit: Readonly<ConcurrencyLimit>; toString(): string; }
/** contract: Approval zero timeout preserves indefinite waits where the owner can wait. */
export interface ApprovalOptions { message?: string; timeoutMs?: number; onTimeout?: "fail" | "deny" | "approve"; }
/** contract: Approvals cannot declare concurrency groups; put them on the job after the approval. skipIf runs on the run's coordinator before requesting approval, holding no capacity, and true settles as skipped. */
export interface Approval { readonly id: string; skipIf(predicate: Predicate, options?: SkipOptions): this; needs(...jobs: readonly Dependency[]): this; needsIfPresent(...jobs: readonly Dependency[]): this; needsGenerated(...generators: readonly Generator<unknown>[]): this; optional(): this; continueOnError(): this; onFailure<T = unknown>(id: string, body: Body<T>): Job<T>; }
/** contract: Dependencies refer to declared jobs, approvals, groups or stable IDs. */
export type Dependency = Job<unknown> | Approval | JobGroup | string;
/** contract: Startup env is applied before interpreter initialization. */
export interface EnvOption { readonly phase: "startup"; }
/** safety: Startup values must not influence declarations; drift fails the attempt. */
export declare function startup(): EnvOption;
/** contract: Step policy is independent of optional job failure. */
export type FailurePolicy = "fail_fast" | "collect_all";
/** contract: Step handles retain output type across dependency reads. */
export declare class Step<T = unknown> {
  private constructor();
  readonly id: string;
  needs(...steps: readonly (Step<unknown> | StepGroup | string)[]): this;
  result(): this;
  skipIf(predicate: Predicate): this;
  finally(): this;
  continueOnError(): this;
  optional(): this;
  dryRun(body: Body<T>): this;
  safeWithoutDryRun(): this;
  /** safety: Every host enforces risk labels before real work. */
  risk(...labels: readonly string[]): this;
}
/** contract: Group dependencies expand to all members and modifiers apply to each member. */
export interface StepGroup {
  readonly id: string;
  readonly members: readonly Step<unknown>[];
  needs(...steps: readonly (Step<unknown> | StepGroup | string)[]): this;
  skipIf(predicate: Predicate): this;
}
/** contract: Display grouping can also name a set of job dependencies. */
export interface JobGroup {
  readonly id: string;
  readonly members: readonly Job<unknown>[];
  needs(...jobs: readonly Dependency[]): this;
  needsIfPresent(...jobs: readonly Dependency[]): this;
  needsGenerated(...generators: readonly Generator<unknown>[]): this;
  retry(attempts: number, options?: RetryOptions): this;
  timeout(ms: number): this;
  noProgressTimeout(ms: number): this;
  skipIf(predicate: Predicate, options?: SkipOptions): this;
  beforeRun(hook: Hook): this;
  verify(hook: Hook): this;
  afterRun(hook: AfterRunFn): this;
  memo(key: MemoInput | MemoKeyFn, options?: MemoOptions): this;
  cache(...caches: readonly CacheSpec[]): this;
  artifacts(...globs: readonly string[]): this;
  consumes(producer: Job<unknown>, options?: { into?: string }): this;
  concurrency(group: ConcurrencyGroup, cost?: number): this;
  needsTools(...names: readonly string[]): this;
  requires(...labels: readonly string[]): this;
  prefers(...labels: readonly string[]): this;
  inline(): this;
  optional(): this;
  continueOnError(): this;
  env(name: string, value: string, option?: EnvOption): this;
}
/** contract: Output reads carry an explicit dependency or reference authorization edge. */
export interface Ref<T> {
  readonly jobId: string;
  get(ctx: Context): Promise<T>;
  /** safety: Only genuine absence returns undefined; decode, transport and cancellation errors reject. */
  tryGet(ctx: Context): Promise<T | undefined>;
}
/** contract: RefToLastRun pins the latest successful output independently of retention. */
export interface PipelineRef<T> extends Ref<T> { readonly pipeline: string; readonly jobId: string; readonly maxAgeMs?: number; }
/** contract: Max age lets a consumer refuse a stale successful result. */
export interface RefOptions { maxAgeMs?: number; }
/** contract: A job output reference retains its declared result type. */
export declare function refTo<T>(job: Job<T>): Ref<T>;
/** contract: Declarative references let the host authorize cross-pipeline reads. */
export declare function refToLastRun<T>(pipeline: string, jobId: string, options?: RefOptions): PipelineRef<T>;
/** contract: Child inputs are explicit and are not inherited from the parent. */
export interface ChildOptions<In extends object = Record<string, string | number | boolean | readonly string[]>> {
  args?: Args;
  /** contract: Input property names are argument names; explicit args override overlapping inputs. */
  inputs?: { readonly [K in keyof In]: In[K] extends string | number | boolean | readonly string[] ? In[K] : never };
  /** contract: Omission inherits parent repo/SHA; an explicit repo selects fresh branch-tip source. */
  repo?: string;
  /** contract: Omission uses main for an explicit repo, otherwise the parent branch. */
  branch?: string;
  /** contract: Includes queue time; expiry stops waiting without cancelling the child. */
  timeoutMs?: number;
}
/** contract: Cleanup survives SDK process loss because the host records argv first. */
export interface CleanupSpec { argv: readonly [string, ...string[]]; description?: string; }
/** contract: Disarming removes a recorded cleanup after the resource was released. */
export interface CleanupRegistration { readonly id: string; unregister(): Promise<void>; }
/** contract: Exec inherits attempt env, including body env, without protocol credentials. */
export interface ExecOptions { cwd?: string; env?: Readonly<Record<string, string>>; }
/** contract: Command output and usage events share one execution helper. */
export interface ExecResult { readonly command: string; readonly stdout: string; readonly stderr: string; readonly exitCode: number; }
/** contract: Opaque cache descriptors prevent authors from selecting privileged ref scopes. */
export interface CacheSpec { readonly kind: "go_modules" | "npm" | "dir"; readonly path?: string; readonly keyFiles?: readonly string[]; }
/** contract: Go cache directories restore through the common depcache path. */
export declare function goModules(): CacheSpec;
/** contract: npm cache directories restore through the common depcache path. */
export declare function npm(): CacheSpec;
/** contract: Custom dependency files contribute to the directory cache key. */
export declare function dir(path: string, ...keyFiles: readonly string[]): CacheSpec;
/** contract: A memo miss sentinel keeps empty keys distinct from disabled reuse. */
export declare const noMemo: unique symbol;
/** contract: Author callbacks return JSON values for host hashing, or noMemo to bypass. */
export type MemoKeyFn = (ctx: Context) => Awaitable<unknown>;
/** contract: Built-in memo recipes are evaluated by the executing host. */
export type MemoInput =
  | { readonly kind: "const"; readonly value: string }
  | { readonly kind: "files" | "repo_files"; readonly patterns: readonly string[] }
  | { readonly kind: "tree"; readonly root: string }
  | { readonly kind: "env"; readonly names: readonly string[] }
  | { readonly kind: "parts"; readonly parts: readonly unknown[] }
  | { readonly kind: "compose"; readonly inputs: readonly MemoInput[] };
/** contract: Excludes matching tracked paths. */
export interface RepoFilesOption {
  readonly ignore: readonly string[];
}
/** contract: Basenames match anywhere, trailing slashes match a root prefix, and ** spans segments. */
export declare function ignore(...patterns: readonly string[]): RepoFilesOption;
/** contract: Matching tracked files contribute to host-computed memo identity. */
export declare function files(...globs: readonly string[]): MemoInput;
/** contract: The effective job environment distinguishes unset from empty. */
export declare function env(...names: readonly string[]): MemoInput;
/** contract: Declares ordered JSON values for host hashing. */
export declare function parts(...values: readonly unknown[]): MemoInput;
/** contract: Declares a nonempty literal string, validated by the host. */
export declare function constant(value: string): MemoInput;
/** contract: Regular files contribute recursively inside the checkout, skipping symlinks; missing roots fail on the host. */
export declare function tree(path: string): MemoInput;
/** contract: Repository-relative selection includes all tracked files except declared exclusions. */
export declare function repoFiles(...options: readonly RepoFilesOption[]): MemoInput;
/** contract: Combines input recipes in order. */
export declare function compose(...inputs: readonly MemoInput[]): MemoInput;
/** contract: Collects ordered JSON parts for host hashing. */
export declare function memoKey(...parts: readonly unknown[]): readonly unknown[] | typeof noMemo;
/** contract: Generated topology is accepted once; replay only reconstructs code. */
export type GenerateFn<T> = (generated: Generated, output: T, ctx: Context) => Awaitable<void>;
/** contract: A generator handle names the settlement barrier consumed by dependencies. */
export interface Generator<T> { readonly id: string; readonly source: Job<T>; }
/** contract: Generated jobs use the same declaration surface and validator as static jobs. */
export interface Generated { job<T = unknown>(id: string, body?: Body<T>): Job<T>; approval(id: string, options?: ApprovalOptions): Approval; groupJobs(id: string, ...jobs: readonly Job<unknown>[]): JobGroup; }
/** contract: Jobs declare execution policy; hosts own placement, validation and persistence. */
export declare class Job<T = unknown> {
  private constructor();
  readonly id: string;
  needs(...jobs: readonly Dependency[]): this;
  needsIfPresent(...jobs: readonly Dependency[]): this;
  needsGenerated(...generators: readonly Generator<unknown>[]): this;
  references(...refs: readonly PipelineRef<unknown>[]): this;
  withArgs(...args: readonly ArgSpec[]): this;
  /** Declare child pipelines in the parent repository. Calls append targets. */
  startsChildren(...pipelines: readonly string[]): this;
  /** Declare child pipelines using a canonical repository key, such as gh:123. */
  startsChildrenIn(repository: string, ...pipelines: readonly string[]): this;
  step<U = unknown>(id: string, body: Body<U>): Step<U>;
  groupSteps(id: string, ...steps: readonly Step<unknown>[]): StepGroup;
  failurePolicy(policy: FailurePolicy): this;
  retry(attempts: number, options?: RetryOptions): this;
  timeout(ms: number): this;
  noProgressTimeout(ms: number): this;
  skipIf(predicate: Predicate, options?: SkipOptions): this;
  beforeRun(hook: Hook): this;
  verify(hook: Hook): this;
  afterRun(hook: AfterRunFn): this;
  memo(key: MemoInput | MemoKeyFn, options?: MemoOptions): this;
  cache(...caches: readonly CacheSpec[]): this;
  artifacts(...globs: readonly string[]): this;
  consumes(producer: Dependency, options?: { into?: string }): this;
  onFailure<U = unknown>(id: string, body: Body<U>): Job<U>;
  concurrency(group: ConcurrencyGroup, cost?: number): this;
  needsTools(...names: readonly string[]): this;
  requires(...labels: readonly string[]): this;
  prefers(...labels: readonly string[]): this;
  inline(): this;
  optional(): this;
  continueOnError(): this;
  env(name: string, value: string, option?: EnvOption): this;
  generate(fn: GenerateFn<T>): Generator<T>;
}
/** contract: Checkout omission inherits source policy; fullHistory requests all history explicitly. */
export interface Checkout { depth?: number; fullHistory?: boolean; tags?: boolean; submodules?: boolean; lfs?: boolean; }
/** contract: Plan knobs are SDK-owned inputs resolved at the second seal point. */
export declare class Plan {
  constructor();
  job<T = unknown>(id: string, body?: Body<T>): Job<T>;
  approval(id: string, options?: ApprovalOptions): Approval;
  groupJobs(id: string, ...jobs: readonly Job<unknown>[]): JobGroup;
  concurrency(group: ConcurrencyGroup, cost?: number): this;
  priority(value: number): this;
  claimWait(ms: number): this;
  admissionClass(value: string): this;
  checkout(options: Checkout): this;
}
/** contract: Names and secret declarations belong to describe, separate from plan documents. */
export interface PipelineDef { name: string; description?: string; short?: string; help?: string; examples?: readonly UsageExample[]; envVars?: readonly EnvironmentDescription[]; extra?: boolean; args?: readonly ArgSpec[]; secrets?: readonly SecretSpec[]; plan(plan: Plan, run: RunContext): Awaitable<void>; }
/** contract: Registration exposes one entrypoint for host-side YAML alias resolution. */
export declare function definePipeline(definition: PipelineDef): PipelineDef;
/** safety: Main negotiates hello before handling any operation or opening bound routes. */
export declare function main(): Promise<void>;

/** contract: Help examples preserve command text and its reason for running. */
export interface UsageExample { command: string; comment?: string; }
/** contract: Environment metadata documents author inputs, never transport credentials. */
export interface EnvironmentDescription { name: string; description?: string; default?: string; }
/** contract: Failed launch and signal termination both use -1, distinguished by ExecError.terminated. */
export declare const exitNotStarted: -1;
/** contract: Command failure preserves output and termination cause for diagnostics. */
export declare class ExecError extends Error { constructor(result: ExecResult, terminated?: "" | "cancellation" | "signal", cause?: unknown); readonly command: string; readonly stdout: string; readonly stderr: string; readonly exitCode: number; readonly cause?: unknown; readonly terminated: "" | "cancellation" | "signal"; }
/** contract: A command executes only when a result method is called. */
export interface Cmd {
  dir(path: string): this;
  env(name: string, value: string): this;
  envMap(values: Readonly<Record<string, string>>): this;
  run(): Promise<ExecResult>;
  capture(): Promise<ExecResult>;
  string(): Promise<string>;
  lines(): Promise<string[]>;
  json<T = unknown>(): Promise<T>;
  mustBeEmpty(reason: string): Promise<void>;
}
/** contract: Root-relative paths match command cwd and file helper resolution. */
export declare function workDir(): string;
/** contract: Absolute-first joins work even without a discovered project. */
export declare function path(...parts: readonly string[]): string;
/** contract: Relative reads use the pipeline root rather than a command's temporary cwd. */
export declare function readFile(path: string): Promise<Uint8Array>;
/** contract: Writes create or truncate with mode 0644, matching the Go helper. */
export declare function writeFile(path: string, data: Uint8Array): Promise<void>;
/** contract: Glob returns absolute matches for *, ? and character classes, without recursive **. */
export declare function glob(pattern: string): Promise<string[]>;
/** contract: Missing project roots are distinguishable from ordinary filesystem errors. */
export declare class NoProjectError extends Error {}
/** contract: Git snapshots stay sealed while live methods inspect the current working tree. */
export declare class Git implements GitInfo {
  constructor(workDir: string, facts: GitInfo);
  readonly repo: string; readonly branch: string; readonly commit: string;
  readonly defaultBranch?: string; readonly repoURL?: string;
  workDir(): string;
  shortSHA(): string;
  name(): string;
  isDirty(ctx: Context): Promise<boolean>;
  filesetHash(ctx: Context): Promise<string>;
  changedFiles(ctx: Context, since: string): Promise<string[]>;
  tagsAtHead(ctx: Context): Promise<string[]>;
  latestTag(ctx: Context, prefix: string): Promise<string>;
  pushTag(ctx: Context, tag: string, message: string): Promise<void>;
}
/** contract: Explicit tree discovery supports author-owned sibling checkouts. */
export declare function gitFromTree(ctx: Context, workDir: string): Promise<Git>;
/** contract: Slug parsing keeps owner/repo handling identical across helper packages. */
export declare function githubOwnerRepo(slug: string): { owner: string; repo: string };
/** contract: Git helpers use promptless direct git and never probe a removed cache service. */
export declare namespace git {
  interface CloneOptions { depth?: number; }
  function clone(ctx: Context, url: string, destDir: string, options?: CloneOptions): Promise<void>;
  function fetch(ctx: Context, repoDir: string): Promise<void>;
  function shortCommit(ctx: Context, repoDir: string): Promise<string>;
  function currentSHA(ctx: Context, repoDir: string): Promise<string>;
  function currentBranch(ctx: Context, repoDir: string): Promise<string>;
  function defaultBranch(ctx: Context, repoDir: string): Promise<string>;
  function remoteOriginURL(ctx: Context, repoDir: string): Promise<string>;
  function isDirty(ctx: Context, repoDir: string): Promise<boolean>;
  function filesetHash(ctx: Context, repoDir: string): Promise<string>;
  function changedFiles(ctx: Context, repoDir: string, since: string): Promise<string[]>;
  function tagsAtHead(ctx: Context, repoDir: string): Promise<string[]>;
  function latestTag(ctx: Context, repoDir: string, prefix: string): Promise<string>;
  function tagExistsOnRemote(ctx: Context, repoDir: string, tag: string): Promise<boolean>;
  function pushTag(ctx: Context, repoDir: string, tag: string, message: string): Promise<void>;
}
/** contract: Docker helpers retain CLI behavior and host-registered cleanup under DinD. */
export declare namespace docker {
  interface BuildConfig { image: string; dockerfile?: string; context?: string; platforms?: readonly string[]; registries?: readonly string[]; tags: readonly string[]; buildArgs?: Readonly<Record<string, string>>; labels?: Readonly<Record<string, string>>; }
  interface BuildResult { readonly image: string; readonly digests: Readonly<Record<string, string>>; readonly registries: readonly string[]; }
  interface RunOptions { image: string; cmd?: readonly string[]; workDir?: string; env?: Readonly<Record<string, string>>; user?: string; inputDir?: string; outputDir?: string; outputTo?: string; volumes?: Readonly<Record<string, string>>; stdout?: (bytes: Uint8Array) => void; stderr?: (bytes: Uint8Array) => void; }
  interface ImageTag { readonly commit: string; readonly content: string; readonly branch: string; readonly dirty: boolean; deployTag(): string; prodTag(): string; all(): string[]; }
  class DockerUnavailableError extends Error {}
  class BuildxRequiredError extends Error {}
  class PlatformUnsupportedError extends Error {}
  function build(ctx: Context, config: BuildConfig): Promise<BuildResult>;
  function buildAndPush(ctx: Context, config: BuildConfig): Promise<BuildResult>;
  function push(ctx: Context, image: string, tags: readonly string[], registries: readonly string[]): Promise<void>;
  function login(ctx: Context, registry: string, username: string, secret: string): Promise<void>;
  function buildxPlatforms(ctx: Context): Promise<string[]>;
  function filterBuildxPlatforms(ctx: Context, wish: readonly string[]): Promise<string[]>;
  function run(ctx: Context, options: RunOptions): Promise<void>;
  function computeTags(ctx: Context): Promise<ImageTag>;
  function computeTagsIn(ctx: Context, repoDir: string): Promise<ImageTag>;
}
/** contract: Service scopes tear down owned containers on success, failure and cancellation. */
export declare namespace services {
  const autoPort: -1;
  const defaultReadyTimeoutMs: 30000;
  interface Service { image: string; name?: string; port?: number; hostPort?: number; env?: Readonly<Record<string, string>>; readyCmd?: string; readyTimeoutMs?: number; }
  interface Addr { readonly name: string; readonly hostPort: number; }
  function withServices(ctx: Context, specs: readonly Service[], fn: (ctx: Context) => Awaitable<void>): Promise<void>;
  function withServicesAddrs(ctx: Context, specs: readonly Service[], fn: (ctx: Context, addrs: readonly Addr[]) => Awaitable<void>): Promise<void>;
}

/** contract: Context-level command env is inherited until explicit command options override it. */
export declare function withCommandEnv(ctx: Context, env: Readonly<Record<string, string>>): Context;
/** contract: Input errors distinguish invalid author arguments from execution failures. */
export declare class InputError extends Error { constructor(cause: unknown); readonly cause: unknown; }
/** contract: Verification errors preserve postcondition failure classification. */
export declare class VerifyError extends Error { constructor(cause: unknown); readonly cause: unknown; }
/** contract: Step errors preserve the failing step and its underlying failure. */
export declare class StepError extends Error { constructor(stepId: string, cause: unknown); readonly stepId: string; readonly cause: unknown; }
