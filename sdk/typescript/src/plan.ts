// The plan DSL. A pipeline's plan function declares jobs, their dependencies
// and envelope; the engine reads the resulting plan document and schedules
// it. Bodies and closures stay in this process and run when the engine asks.

import { posix } from "node:path";
import type { LogWriter } from "./log.ts";
import {
  PROTOCOL_VERSION,
  type ApprovalSpec,
  type CacheSpec,
  type Closures,
  type Modifiers,
  type PlanConcurrency,
  type PlanDoc,
  type PlanNode,
  type PlanStep,
} from "./describe.ts";

export interface Trigger {
  source: "manual" | "push" | "schedule" | "webhook";
  user?: string;
  pull_request?: {
    number: number;
    action?: string;
    base_ref?: string;
    base_sha?: string;
    head_ref?: string;
    head_sha?: string;
  };
}

export interface GitInfo {
  sha: string;
  branch: string;
  default_branch?: string;
  repo?: string;
  repo_url?: string;
}

/** The run a plan is built for, from the plan request's run context. */
export interface RunContext {
  runId: string;
  pipeline: string;
  args: Record<string, string>;
  startedAt?: string;
  trigger?: Trigger;
  git?: GitInfo;
  dryRun: boolean;
  noCache: boolean;
  log: LogWriter;
}

/** How the node an onFailure job recovers from failed. */
export interface Failure {
  node: string;
  stage: "action" | "verify";
  message: string;
}

export interface NodeContext {
  runId: string;
  nodeId: string;
  attempt: number;
  args: Record<string, string>;
  dryRun: boolean;
  log: LogWriter;
  /** Aborts when the engine cancels this step or stops asking. */
  signal: AbortSignal;
  /** Set only in the body of an onFailure job. */
  failure?: Failure;
  secret(name: string): Promise<string>;
  /** Another node's output, read through the engine's output route. */
  output<T = unknown>(nodeId: string): Promise<T | null>;
  /** The output of a step of this node that already ran in this process. */
  stepOutput<T = unknown>(stepId: string): T | undefined;
}

export type Body = (ctx: NodeContext) => unknown;
export type Predicate = (ctx: NodeContext) => boolean | Promise<boolean>;
/** A beforeRun or verify closure; throwing fails it. */
export type Hook = (ctx: NodeContext) => unknown;
/** Gets the node's final error, or undefined when the node succeeded. */
export type AfterRunFn = (ctx: NodeContext, error: Error | undefined) => unknown;
/** Returns the memo key, or null to run without memo. */
export type CacheKeyFn = (ctx: NodeContext) => string | null | Promise<string | null>;

/** Adds the nodes of a dynamic fan-out from its source node's output. */
export type GenerateFn<T = unknown> = (gen: Generated, output: T | null, ctx: NodeContext) => void | Promise<void>;

interface ClosureBase {
  id: string;
  job: Job;
  step?: Step;
}

/** A closure the engine evaluates by id. */
export type Closure = ClosureBase &
  (
    | { kind: "skip_if"; fn: Predicate }
    | { kind: "before_run" | "verify"; fn: Hook }
    | { kind: "after_run"; fn: AfterRunFn }
    | { kind: "cache_key"; fn: CacheKeyFn }
    | { kind: "generator"; gen: Generator }
  );

export interface RetryOptions {
  backoffMs?: number;
  /** Retries by starting the whole node again rather than its failed steps. */
  auto?: boolean;
}

export interface SkipOptions {
  /** How long the engine lets each skipIf of the node run; 30 seconds when absent or 0. */
  budgetMs?: number;
}

export interface MemoizeOptions {
  /** How long a memoized result stays reusable; 7 days by default and at most 35. */
  ttlMs?: number;
}

export interface ResourceHints {
  cores?: number;
  memoryGB?: number;
}

export interface ApprovalOptions {
  message?: string;
  /** How long the gate waits for an answer; it waits forever when absent. */
  timeoutMs?: number;
  /** What an unanswered gate does when timeoutMs passes; fail by default. */
  onTimeout?: "fail" | "deny" | "approve";
}

export interface ConcurrencyLimit {
  /** Total cost the group admits at once; 1 when absent. */
  capacity?: number;
  /** Whose holders share the group: one run, one machine, or everyone (the default). */
  scope?: "run" | "box" | "global";
  onLimit?: "queue" | "fail" | "skip" | "cancel_others";
  queueTimeoutMs?: number;
  cancelTimeoutMs?: number;
}

const DAY_MS = 86_400_000;
export const DEFAULT_CACHE_TTL_MS = 7 * DAY_MS;
export const MAX_CACHE_TTL_MS = 35 * DAY_MS;

const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

function checkId(kind: string, id: string): void {
  if (!idPattern.test(id)) throw new Error(`${kind} id ${JSON.stringify(id)} must match ${idPattern}`);
}

function checkInt(what: string, v: number, min: number): number {
  if (!Number.isInteger(v) || v < min) throw new Error(`${what} must be an integer of at least ${min}`);
  return v;
}

function labels(values: string[]): string[] | undefined {
  const out = values.filter((l) => l !== "");
  return out.length > 0 ? out : undefined;
}

function resourceFields(what: string, h: ResourceHints): { cores?: number; memory_bytes?: number } {
  const out: { cores?: number; memory_bytes?: number } = {};
  for (const [k, v] of Object.entries(h)) {
    if (v === undefined) continue;
    if (typeof v !== "number" || !Number.isFinite(v) || v <= 0) throw new Error(`${what}: ${k} must be a positive, finite number`);
  }
  if (h.cores !== undefined) out.cores = h.cores;
  if (h.memoryGB !== undefined) out.memory_bytes = Math.round(h.memoryGB * 2 ** 30);
  return out;
}

// safety: like the Go SDK, a later call overrides only the dimensions it
// names, so a reservation another call set is never dropped silently.
function mergeResources(
  current: { cores?: number | undefined; memory_bytes?: number | undefined },
  what: string,
  hints: ResourceHints,
): { cores?: number; memory_bytes?: number } | undefined {
  const r = resourceFields(what, hints);
  if (Object.keys(r).length === 0) return undefined;
  const out: { cores?: number; memory_bytes?: number } = {};
  const cores = r.cores ?? current.cores;
  const memory = r.memory_bytes ?? current.memory_bytes;
  if (cores !== undefined) out.cores = cores;
  if (memory !== undefined) out.memory_bytes = memory;
  return out;
}

/** A named admission budget that jobs or whole runs hold while they run. */
export class ConcurrencyGroup {
  readonly name: string;
  readonly limit: Readonly<ConcurrencyLimit>;

  constructor(name: string, limit: ConcurrencyLimit = {}) {
    if (name === "") throw new Error("a concurrency group needs a name, or every unnamed group would share one budget");
    if (limit.capacity !== undefined) checkInt(`concurrency group ${name}: capacity`, limit.capacity, 1);
    this.name = name;
    this.limit = { ...limit };
  }

  cost(subject: string, cost: number): number {
    checkInt(`${subject}: concurrency cost`, cost, 1);
    const capacity = this.limit.capacity ?? 1;
    if (cost > capacity)
      throw new Error(`${subject}: cost ${cost} exceeds group ${this.name} capacity ${capacity}, so it could never be admitted`);
    return cost;
  }
}

/** The go-modules dependency cache, keyed by go.sum. */
export function goModulesCache(): CacheSpec {
  return { name: "go-modules", resolver: "gomod", key_files: ["go.sum"] };
}

/** The npm dependency cache, keyed by package-lock.json. */
export function npmCache(): CacheSpec {
  return { name: "npm", resolver: "npm", key_files: ["package-lock.json"] };
}

/** A directory cache keyed by the first of keyFiles that exists. */
export function dirCache(path: string, ...keyFiles: string[]): CacheSpec {
  if (path === "") throw new Error("dirCache: the path must not be empty");
  const clean = posix.normalize(path).replace(/(.)\/+$/, "$1");
  return { name: posix.basename(clean), path, key_scope: clean, key_files: keyFiles };
}

export class Step {
  readonly id: string;
  readonly body: Body;
  readonly skipIfs: Predicate[] = [];
  #dryRun: Body | undefined;
  readonly #needs: string[] = [];
  readonly #flags: Pick<PlanStep, "finally" | "continue_on_error" | "optional" | "safe_without_dry_run"> = {};
  #result = false;

  constructor(id: string, body: Body) {
    checkId("step", id);
    this.id = id;
    this.body = body;
  }

  needs(...steps: Array<Step | string>): this {
    for (const s of steps) this.#needs.push(typeof s === "string" ? s : s.id);
    return this;
  }

  /** Makes this step's output the node's output; a job has at most one result step. */
  result(): this {
    this.#result = true;
    return this;
  }

  /** Adds a predicate the engine evaluates before this step; true skips it. */
  skipIf(fn: Predicate): this {
    this.skipIfs.push(fn);
    return this;
  }

  /** Runs this step last, even after a sibling failed. */
  finally(): this {
    this.#flags.finally = true;
    return this;
  }

  /**
   * Keeps siblings running and lets steps that need this one start after it
   * fails; the node still fails unless the step is also optional.
   */
  continueOnError(): this {
    this.#flags.continue_on_error = true;
    return this;
  }

  /** Keeps this step's failure out of the node's outcome; implies continueOnError. */
  optional(): this {
    this.#flags.optional = true;
    this.#flags.continue_on_error = true;
    return this;
  }

  /** Runs in place of the body in a dry run; a step with neither this nor safeWithoutDryRun is skipped there. */
  dryRun(body: Body): this {
    this.#dryRun = body;
    return this;
  }

  /** Runs the body unchanged in a dry run. */
  safeWithoutDryRun(): this {
    this.#flags.safe_without_dry_run = true;
    return this;
  }

  get dryRunBody(): Body | undefined {
    return this.#dryRun;
  }

  get needIds(): readonly string[] {
    return this.#needs;
  }

  get isResult(): boolean {
    return this.#result;
  }

  toPlanStep(closures: Closures | undefined): PlanStep {
    const step: PlanStep = { id: this.id };
    if (this.#needs.length > 0) step.needs = [...this.#needs];
    if (this.#result) step.is_result = true;
    if (this.skipIfs.length > 0) step.has_skip_if = true;
    Object.assign(step, this.#flags);
    if (this.#dryRun) step.has_dry_run = true;
    if (closures) step.closures = closures;
    return step;
  }
}

/** A generator closure of a job; pass it to needsGenerated to wait for its nodes. */
export class Generator {
  readonly id: string;
  readonly source: Job;
  readonly fn: GenerateFn<never>;

  constructor(id: string, source: Job, fn: GenerateFn<never>) {
    this.id = id;
    this.source = source;
    this.fn = fn;
  }
}

/** Collects the jobs a generator adds; each one depends on the generator's source. */
export class Generated {
  readonly jobs: Job[] = [];
  readonly #source: Job;

  constructor(source: Job) {
    this.#source = source;
  }

  job(id: string, body?: Body): Job {
    if (this.jobs.some((j) => j.id === id)) throw new Error(`duplicate job ${id}`);
    const j = new Job(id, body).needs(this.#source);
    this.jobs.push(j);
    return j;
  }
}

export class Job {
  readonly id: string;
  readonly approval: ApprovalSpec | undefined;
  readonly steps: Step[] = [];
  readonly skipIfs: Predicate[] = [];
  readonly beforeRuns: Hook[] = [];
  readonly afterRuns: AfterRunFn[] = [];
  readonly generators: Generator[] = [];
  #verify: Hook | undefined;
  #cacheKey: CacheKeyFn | undefined;
  #recovery: Job | undefined;
  #recoveryOf: string | undefined;
  #deps: string[] = [];
  #needsGenerated: string[] = [];
  #mods: Modifiers = {};
  #env: Record<string, string> | undefined;
  #caches: CacheSpec[] = [];
  #failurePolicy: "fail-fast" | "collect-all" | undefined;
  #hasBody = false;

  // safety: a body is the job's one step, run, as the Go SDK writes it, so the
  // host applies step rules to it; a dry run skips it like any unmarked step.
  constructor(id: string, body?: Body, approval?: ApprovalSpec) {
    checkId("job", id);
    this.id = id;
    this.approval = approval;
    if (body) {
      this.steps.push(new Step("run", body).result());
      this.#hasBody = true;
    }
  }

  /** Runs this job only after every named job succeeds. */
  needs(...jobs: Array<Job | string>): this {
    for (const j of jobs) this.#deps.push(typeof j === "string" ? j : j.id);
    return this;
  }

  /** Runs this job only after every node each generator adds has finished. */
  needsGenerated(...gens: Generator[]): this {
    for (const g of gens) this.#needsGenerated.push(g.id);
    return this;
  }

  /** Lets the engine start the job again, up to attempts more times, after a failure. */
  retry(attempts: number, opts: RetryOptions = {}): this {
    checkInt(`job ${this.id}: retry attempts`, attempts, 0);
    if (opts.backoffMs !== undefined) checkInt(`job ${this.id}: retry backoff in milliseconds`, opts.backoffMs, 0);
    this.#set({ retry: attempts, retry_backoff_ms: opts.backoffMs, retry_auto: opts.auto });
    return this;
  }

  /** Bounds one attempt's wall time; the engine kills the node process when it passes. */
  timeout(ms: number): this {
    this.#set({ timeout_ms: checkInt(`job ${this.id}: timeout in milliseconds`, ms, 1) });
    return this;
  }

  /** Bounds how long an attempt may write no log record; 0 removes the bound. */
  noProgressTimeout(ms: number): this {
    this.#set({ no_progress_timeout_ms: checkInt(`job ${this.id}: no-progress timeout in milliseconds`, ms, 0) });
    return this;
  }

  /** Adds a step; the engine runs each step with its own request, in dependency order. */
  step(id: string, body: Body): Step {
    if (this.#hasBody || this.approval) throw new Error(`job ${this.id} has a body, so it cannot also have steps`);
    if (this.steps.some((s) => s.id === id)) throw new Error(`job ${this.id}: duplicate step ${id}`);
    const s = new Step(id, body);
    this.steps.push(s);
    return s;
  }

  /** Lets independent steps finish after one fails, instead of cancelling them. */
  failurePolicy(policy: "fail-fast" | "collect-all"): this {
    this.#failurePolicy = policy;
    return this;
  }

  /** Adds a predicate the engine evaluates by closure id before starting the job; true skips it. */
  skipIf(fn: Predicate, opts: SkipOptions = {}): this {
    this.skipIfs.push(fn);
    if (opts.budgetMs !== undefined)
      this.#set({ skip_if_budget_ms: checkInt(`job ${this.id}: skipIf budget in milliseconds`, opts.budgetMs, 0) });
    return this;
  }

  /** Runs before the body on the first attempt; throwing fails the node without a retry. */
  beforeRun(fn: Hook): this {
    this.beforeRuns.push(fn);
    return this;
  }

  /** Runs after each successful body; throwing fails the attempt, so retry and onFailure apply. */
  verify(fn: Hook): this {
    this.#verify = fn;
    return this;
  }

  /** Runs once the node is done, after its last attempt; it cannot change the outcome. */
  afterRun(fn: AfterRunFn): this {
    this.afterRuns.push(fn);
    return this;
  }

  /** Lets the engine reuse an earlier run's result for the same key instead of running the job. */
  memoize(key: CacheKeyFn, opts: MemoizeOptions = {}): this {
    const ttl = opts.ttlMs !== undefined && opts.ttlMs > 0 ? Math.min(Math.floor(opts.ttlMs), MAX_CACHE_TTL_MS) : DEFAULT_CACHE_TTL_MS;
    this.#cacheKey = key;
    this.#set({ cache: true, cache_ttl_ms: ttl });
    return this;
  }

  /** Restores each directory before the node starts and saves it after the node succeeds. */
  cacheDir(...caches: CacheSpec[]): this {
    for (const c of caches) {
      if (!c.path && !c.resolver) throw new Error(`job ${this.id}: cache ${c.name} needs a path or a resolver`);
      if (c.key_files.length === 0) throw new Error(`job ${this.id}: cache ${c.name} needs at least one key file`);
      this.#caches.push({ ...c, key_files: [...c.key_files] });
    }
    return this;
  }

  /** Runs a recovery job with this id when this job fails; its body reads ctx.failure. */
  onFailure(id: string, body: Body): this {
    const rec = new Job(id, body);
    rec.#recoveryOf = this.id;
    this.#recovery = rec;
    this.#set({ on_failure: id });
    return this;
  }

  /** Holds cost units of the group while the job runs. */
  concurrency(group: ConcurrencyGroup, cost = 1): this {
    const l = group.limit;
    this.#set({
      conc_group: group.name,
      conc_capacity: l.capacity,
      conc_cost: group.cost(`job ${this.id}`, cost),
      conc_scope: l.scope,
      conc_on_limit: l.onLimit,
      conc_queue_timeout_ms: l.queueTimeoutMs,
      conc_cancel_timeout_ms: l.cancelTimeoutMs,
    });
    return this;
  }

  /**
   * Declares the job's peak cores and memory, which admission reserves; each
   * call keeps the dimensions it omits, and no hints clear both.
   */
  resources(hints: ResourceHints): this {
    const current = { cores: this.#mods.res_cores, memory_bytes: this.#mods.res_memory_bytes };
    const r = mergeResources(current, `job ${this.id}`, hints);
    this.#set({ res_cores: r?.cores, res_memory_bytes: r?.memory_bytes });
    return this;
  }

  /** Runs the job only on a runner with these labels; "a,b" means a or b. */
  requires(...runnerLabels: string[]): this {
    this.#set({ runs_on: labels(runnerLabels) });
    return this;
  }

  /** Prefers a runner with these labels while one has a free slot. */
  prefers(...runnerLabels: string[]): this {
    this.#set({ prefers: labels(runnerLabels) });
    return this;
  }

  /** Lets the jobs that need this one start after it fails; the run still fails unless the job is also optional. */
  continueOnError(): this {
    this.#set({ continue_on_error: true });
    return this;
  }

  /** Keeps this job's failure out of the run's outcome; implies continueOnError. */
  optional(): this {
    this.#set({ optional: true, continue_on_error: true });
    return this;
  }

  /** Sets an environment variable in the job's processes. */
  env(key: string, value: string): this {
    this.#env = { ...this.#env, [key]: value };
    return this;
  }

  /**
   * Adds a generator the engine evaluates after this job succeeds. It gets
   * this job's output and adds jobs that run after it. It must add the same
   * jobs for the same output, because each added job's own process runs it
   * again.
   */
  generate<T = unknown>(fn: GenerateFn<T>): Generator {
    const g = new Generator(`${this.id}/generator/${this.generators.length}`, this, fn as GenerateFn<never>);
    this.generators.push(g);
    return g;
  }

  /** The recovery job onFailure declared. */
  get recovery(): Job | undefined {
    return this.#recovery;
  }

  /** Every closure of the job and its steps, with the ids the plan document lists. */
  closures(): Closure[] {
    const job = this;
    const at = (kind: string, i: number) => `${this.id}/${kind}/${i}`;
    const out: Closure[] = [
      ...this.skipIfs.map((fn, i): Closure => ({ id: at("skip_if", i), kind: "skip_if", job, fn })),
      ...this.beforeRuns.map((fn, i): Closure => ({ id: at("before_run", i), kind: "before_run", job, fn })),
      ...this.afterRuns.map((fn, i): Closure => ({ id: at("after_run", i), kind: "after_run", job, fn })),
      ...this.generators.map((gen): Closure => ({ id: gen.id, kind: "generator", job, gen })),
    ];
    if (this.#verify) out.push({ id: at("verify", 0), kind: "verify", job, fn: this.#verify });
    if (this.#cacheKey) out.push({ id: at("cache_key", 0), kind: "cache_key", job, fn: this.#cacheKey });
    for (const step of this.steps) {
      step.skipIfs.forEach((fn, i) => out.push({ id: `${this.id}/step/${step.id}/skip_if/${i}`, kind: "skip_if", job, step, fn }));
    }
    return out;
  }

  toNode(): PlanNode {
    const node: PlanNode = { id: this.id, deps: [...this.#deps] };
    if (this.#env) node.env = { ...this.#env };
    if (this.generators.length > 0) node.dynamic = true;
    if (this.approval) node.approval = { ...this.approval };
    if (this.#recoveryOf !== undefined) node.on_failure_of = this.#recoveryOf;
    const m: Modifiers = { ...this.#mods };
    if (this.skipIfs.length > 0) m.has_skip_if = true;
    if (this.beforeRuns.length > 0) m.has_before_run = true;
    if (this.afterRuns.length > 0) m.has_after_run = true;
    if (this.#verify) m.has_verify = true;
    if (Object.keys(m).length > 0) node.modifiers = m;
    if (this.#caches.length > 0) node.caches = this.#caches.map((c) => ({ ...c }));
    const all = this.closures();
    const nodeClosures = collect(all.filter((c) => !c.step));
    if (nodeClosures?.cache_key) node.memo_key = { closure: nodeClosures.cache_key };
    if (this.steps.length > 0) {
      node.work = { steps: this.steps.map((s) => s.toPlanStep(collect(all.filter((c) => c.step === s)))) };
      const result = this.steps.find((s) => s.isResult);
      if (result) node.work.result_step = result.id;
      if (this.#failurePolicy) node.work.failure_policy = this.#failurePolicy;
    }
    if (nodeClosures) node.closures = nodeClosures;
    if (this.#needsGenerated.length > 0) node.needs_generated = [...this.#needsGenerated];
    return node;
  }

  // safety: the plan document carries no zero or empty modifier, so unset
  // fields are dropped rather than written as undefined.
  #set(fields: { [K in keyof Modifiers]: Modifiers[K] | undefined }): void {
    for (const [k, v] of Object.entries(fields) as Array<[keyof Modifiers, unknown]>) {
      if (v === undefined || v === 0 || v === false || v === "") delete this.#mods[k];
      else (this.#mods as Record<string, unknown>)[k] = v;
    }
  }
}

function collect(closures: Closure[]): Closures | undefined {
  const out: Closures = {};
  for (const c of closures) {
    if (c.kind === "verify" || c.kind === "cache_key") out[c.kind] = c.id;
    else (out[c.kind] ??= []).push(c.id);
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** Each job followed, at the end, by the recovery jobs onFailure declared. */
export function withRecoveries(jobs: Job[]): Job[] {
  return [...jobs, ...jobs.flatMap((j) => (j.recovery ? [j.recovery] : []))];
}

export class Plan {
  readonly jobs: Job[] = [];
  readonly #concurrency: Array<{ group: ConcurrencyGroup; cost: number }> = [];
  #resources: { cores?: number; memory_bytes?: number } | undefined;

  /** Declares a job; pass a body, or none and add steps with job.step(). */
  job(id: string, body?: Body): Job {
    return this.#add(new Job(id, body));
  }

  /** Declares a gate the run waits on until someone approves or denies it. */
  approval(id: string, opts: ApprovalOptions = {}): Job {
    const spec: ApprovalSpec = {};
    if (opts.message) spec.message = opts.message;
    if (opts.timeoutMs !== undefined) spec.timeout_ms = checkInt(`approval ${id}: timeout in milliseconds`, opts.timeoutMs, 1);
    if (opts.onTimeout) spec.on_timeout = opts.onTimeout;
    return this.#add(new Job(id, undefined, spec));
  }

  /** Holds cost units of the group from before the run's first job until the run ends. */
  concurrency(group: ConcurrencyGroup, cost = 1): this {
    if (group.limit.scope === "box") {
      throw new Error(`plan concurrency group ${group.name}: box scope needs the engine's machine id, which this SDK cannot read`);
    }
    const entry = { group, cost: group.cost("plan", cost) };
    const i = this.#concurrency.findIndex((c) => c.group.name === group.name && c.group.limit.scope === group.limit.scope);
    if (i >= 0) this.#concurrency[i] = entry;
    else this.#concurrency.push(entry);
    return this;
  }

  /** Declares the whole run's peak cores and memory; each call keeps the dimensions it omits, and no hints clear both. */
  resources(hints: ResourceHints): this {
    this.#resources = mergeResources(this.#resources ?? {}, "plan", hints);
    return this;
  }

  /** Every job of the plan, recovery jobs last. */
  allJobs(): Job[] {
    return withRecoveries(this.jobs);
  }

  lookup(id: string): Job | undefined {
    return this.allJobs().find((j) => j.id === id);
  }

  /** Validates the graph and returns the plan document the engine schedules. */
  toDoc(pipeline: string, runId: string): PlanDoc {
    const all = this.allJobs();
    const doc: PlanDoc = { protocol: PROTOCOL_VERSION, pipeline, run_id: runId, nodes: checkJobs(all, all) };
    const [first, ...rest] = this.#concurrency.map(({ group, cost }) => planConcurrency(group, cost, runId));
    if (first) {
      doc.plan_concurrency = first;
      doc.plan_concurrency_groups = [first, ...rest];
    }
    if (this.#resources) doc.plan_resources = { ...this.#resources };
    return doc;
  }

  /**
   * Adds the jobs a generator produced, after checking them against the
   * plan, and returns their node documents.
   */
  insert(added: Job[]): PlanNode[] {
    const jobs = withRecoveries(added);
    for (const j of jobs) {
      if (this.lookup(j.id)) throw new Error(`generated job ${j.id} collides with a job of the plan`);
    }
    const nodes = checkJobs(jobs, [...this.allJobs(), ...jobs]);
    this.jobs.push(...added);
    return nodes;
  }

  #add(job: Job): Job {
    if (this.lookup(job.id)) throw new Error(`duplicate job ${job.id}`);
    this.jobs.push(job);
    return job;
  }
}

// safety: the key carries its scope, and a run key the run id's length, so
// run "a" with group "bc" never shares a budget with run "ab" with group "c".
function planConcurrency(group: ConcurrencyGroup, cost: number, runId: string): PlanConcurrency {
  const l = group.limit;
  const key = l.scope === "run" ? `r:${runId.length}:${runId}${group.name}` : `g:${group.name}`;
  const out: PlanConcurrency = { key, cost };
  if (l.capacity) out.capacity = l.capacity;
  if (l.onLimit) out.on_limit = l.onLimit;
  if (l.queueTimeoutMs) out.queue_timeout_ms = l.queueTimeoutMs;
  if (l.cancelTimeoutMs) out.cancel_timeout_ms = l.cancelTimeoutMs;
  return out;
}

function checkJobs(jobs: Job[], all: Job[]): PlanNode[] {
  const seen = new Set<string>();
  for (const j of all) {
    if (seen.has(j.id)) throw new Error(`duplicate job ${j.id}`);
    seen.add(j.id);
  }
  for (const j of jobs) {
    if (!j.approval && j.steps.length === 0) throw new Error(`job ${j.id} has neither a body nor steps`);
    if (j.steps.filter((s) => s.isResult).length > 1) throw new Error(`job ${j.id} has more than one result step`);
    assertAcyclic(
      `job ${j.id} steps`,
      j.steps.map((s) => ({ id: s.id, deps: [...s.needIds] })),
    );
  }
  const generators = new Set(all.flatMap((j) => j.generators.map((g) => g.id)));
  const nodes = jobs.map((j) => j.toNode());
  for (const n of nodes) {
    for (const g of n.needs_generated ?? []) {
      if (!generators.has(g)) throw new Error(`job ${n.id} waits on generator ${g}, which does not exist`);
    }
  }
  assertAcyclic(
    "plan",
    all.map((j) => j.toNode()),
  );
  return nodes;
}

function assertAcyclic(what: string, nodes: Array<{ id: string; deps: string[] }>): void {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  for (const n of nodes) {
    for (const d of n.deps) {
      if (!byId.has(d)) throw new Error(`${what}: ${n.id} needs ${d}, which does not exist`);
    }
  }
  const state = new Map<string, "visiting" | "done">();
  const visit = (id: string, path: string[]): void => {
    if (state.get(id) === "done") return;
    if (state.get(id) === "visiting") throw new Error(`${what}: cycle ${[...path, id].join(" -> ")}`);
    state.set(id, "visiting");
    for (const d of byId.get(id)?.deps ?? []) visit(d, [...path, id]);
    state.set(id, "done");
  };
  for (const n of nodes) visit(n.id, []);
}

export interface ArgSpec {
  name: string;
  type?: "string" | "bool" | "int";
  required?: boolean;
  desc?: string;
  default?: string;
  enum?: string[];
  secret?: boolean;
}

export interface PipelineDef {
  name: string;
  short?: string;
  help?: string;
  args?: ArgSpec[];
  plan(plan: Plan, run: RunContext): void | Promise<void>;
}

/**
 * Checks plan args against the pipeline's declared flags and fills in
 * defaults. It returns the problem as a string so the caller can answer
 * invalid_args.
 */
export function resolveArgs(def: PipelineDef, args: Record<string, string>): Record<string, string> | string {
  const specs = new Map((def.args ?? []).map((a) => [a.name, a]));
  for (const name of Object.keys(args)) {
    if (!specs.has(name)) return `pipeline ${def.name} has no flag --${name}`;
  }
  const out: Record<string, string> = {};
  for (const spec of specs.values()) {
    const v = args[spec.name] ?? spec.default;
    if (v === undefined) {
      if (spec.required) return `--${spec.name} is required`;
      continue;
    }
    if (spec.type === "int" && !/^-?\d+$/.test(v)) return `--${spec.name}: ${JSON.stringify(v)} is not an integer`;
    if (spec.type === "bool" && v !== "true" && v !== "false") return `--${spec.name}: ${JSON.stringify(v)} is not true or false`;
    if (spec.enum && !spec.enum.includes(v)) return `--${spec.name}: ${JSON.stringify(v)} is not one of ${spec.enum.join(", ")}`;
    out[spec.name] = v;
  }
  return out;
}

const registry = new Map<string, PipelineDef>();

/** Registers a pipeline with this process; describe and the runner loop answer for it. */
export function definePipeline(def: PipelineDef): PipelineDef {
  checkId("pipeline", def.name);
  if (registry.has(def.name)) throw new Error(`pipeline ${def.name} is already defined`);
  registry.set(def.name, def);
  return def;
}

export function pipelines(): PipelineDef[] {
  return [...registry.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export function lookupPipeline(name: string): PipelineDef | undefined {
  return registry.get(name);
}

/** Empties the registry; tests use it to start from a known set. */
export function resetPipelines(): void {
  registry.clear();
}
