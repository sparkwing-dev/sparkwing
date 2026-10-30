import { jsonValue } from "./protocol.ts";
import { argument } from "./describe.ts";
import { posix } from "node:path";
import type * as API from "../index.d.ts";
import { failure, SDKError } from "./errors.ts";
import type { ClosureSpec, ConcurrencySpec, JobSpec, PlanDoc, StepSpec } from "./plan-wire.ts";

function identifier(kind: string, value: string): void {
  if (typeof value !== "string" || value.length === 0) throw new Error(`${kind} identifier must not be empty`);
}
function integer(name: string, value: number, min = 0): number {
  if (!Number.isSafeInteger(value) || value < min) throw new Error(`${name} must be a ${min === 0 ? "nonnegative" : "positive"} integer`);
  return value;
}
function unique(values: readonly string[]): string[] {
  return [...new Set(values)];
}
function names(kind: string, values: readonly string[]): string[] {
  for (const value of values) identifier(kind, value);
  return unique(values);
}
function optionalField<T extends object, K extends keyof T>(target: T, key: K, value: T[K] | undefined): void {
  if (value === undefined) delete target[key];
  else target[key] = value;
}
function dependencyIDs(deps: readonly API.Dependency[]): string[] {
  return unique(deps.flatMap((dep) => (typeof dep === "string" ? [dep] : "members" in dep ? dep.members.map((job) => job.id) : [dep.id])));
}
function stepIDs(deps: readonly (API.Step<unknown> | API.StepGroup | string)[]): string[] {
  return unique(
    deps.flatMap((dep) => (typeof dep === "string" ? [dep] : "members" in dep ? dep.members.map((step) => step.id) : [dep.id])),
  );
}

export class ConcurrencyGroup implements API.ConcurrencyGroup {
  readonly name: string;
  readonly limit: Readonly<API.ConcurrencyLimit>;
  constructor(name: string, limit: API.ConcurrencyLimit = {}) {
    identifier("concurrency group", name);
    if (limit.capacity !== undefined) integer("concurrency capacity", limit.capacity, 1);
    if (limit.queueTimeoutMs !== undefined) integer("concurrency queue timeout", limit.queueTimeoutMs);
    if (limit.scope !== undefined && !["global", "run", "box"].includes(limit.scope)) throw new Error("invalid concurrency scope");
    if (limit.onLimit !== undefined && !["queue", "fail", "skip", "cancel_others"].includes(limit.onLimit))
      throw new Error("invalid concurrency onLimit");
    this.name = name;
    this.limit = Object.freeze({ ...limit });
  }
  toString(): string {
    return this.name;
  }
}
function concurrency(group: API.ConcurrencyGroup, cost: number | undefined): ConcurrencySpec {
  const limit = group.limit;
  if (cost !== undefined) integer("concurrency cost", cost, 1);
  if (cost !== undefined && limit.capacity !== undefined && cost > limit.capacity)
    throw new Error(`cost ${cost} exceeds group capacity ${limit.capacity}`);
  const spec: ConcurrencySpec = { group: group.name };
  if (limit.scope !== undefined) spec.scope = limit.scope;
  if (limit.capacity !== undefined) spec.capacity = limit.capacity;
  if (cost !== undefined) spec.cost = cost;
  if (limit.onLimit !== undefined) spec.on_limit = limit.onLimit;
  if (limit.queueTimeoutMs !== undefined) spec.queue_timeout_ms = limit.queueTimeoutMs;
  return spec;
}
function addConcurrency(spec: { concurrency?: ConcurrencySpec[] }, group: API.ConcurrencyGroup, cost: number | undefined): void {
  const entry = concurrency(group, cost);
  const entries = (spec.concurrency ??= []);
  const i = entries.findIndex((other) => other.group === entry.group && other.scope === entry.scope);
  if (i < 0) entries.push(entry);
  else entries[i] = entry;
}

export function startup(): API.EnvOption {
  return { phase: "startup" };
}
export function goModules(): API.CacheSpec {
  return Object.freeze({ kind: "go_modules" });
}
export function npm(): API.CacheSpec {
  return Object.freeze({ kind: "npm" });
}
export function dir(path: string, ...keyFiles: readonly string[]): API.CacheSpec {
  identifier("cache path", path);
  if (!keyFiles.length) throw new Error("directory cache needs at least one key file");
  return Object.freeze({ kind: "dir", path, keyFiles: Object.freeze(names("cache key file", keyFiles)) });
}

interface ClosureBase {
  id: string;
  job: Declaration;
  step?: Step<unknown>;
}
export type Closure = ClosureBase &
  (
    | { kind: "skip_if"; fn: API.Predicate }
    | { kind: "before_run" | "verify"; fn: API.Hook }
    | { kind: "after_run"; fn: API.AfterRunFn }
    | { kind: "memo_key"; fn: API.MemoKeyFn }
    | { kind: "generate"; fn: API.GenerateFn<unknown>; generator: Generator<unknown> }
  );
function collect(closures: readonly Closure[]): ClosureSpec | undefined {
  if (!closures.length) return undefined;
  const spec: ClosureSpec = {};
  for (const closure of closures) {
    if (closure.kind === "verify" || closure.kind === "memo_key") spec[closure.kind] = closure.id;
    else (spec[closure.kind] ??= []).push(closure.id);
  }
  return spec;
}

export class Step<T = unknown> implements API.Step<T> {
  readonly id: string;
  readonly body: API.Body<T>;
  readonly owner: Job<unknown>;
  #spec: StepSpec;
  #skip: API.Predicate[] = [];
  #dryRun: API.Body<T> | undefined;
  #result = false;
  constructor(owner: Job<unknown>, id: string, body: API.Body<T>) {
    identifier("step", id);
    this.owner = owner;
    this.id = id;
    this.body = body;
    this.#spec = { id };
  }
  needs(...steps: readonly (API.Step<unknown> | API.StepGroup | string)[]): this {
    this.assertMutable();
    this.#spec.needs = unique([...(this.#spec.needs ?? []), ...stepIDs(steps)]);
    return this;
  }
  result(): this {
    this.assertMutable();
    this.#result = true;
    return this;
  }
  skipIf(fn: API.Predicate): this {
    this.assertMutable();
    this.#skip.push(fn);
    return this;
  }
  finally(): this {
    this.assertMutable();
    this.#spec.finally = true;
    return this;
  }
  continueOnError(): this {
    this.assertMutable();
    this.#spec.continue_on_error = true;
    return this;
  }
  optional(): this {
    this.assertMutable();
    this.#spec.optional = true;
    return this.continueOnError();
  }
  dryRun(body: API.Body<T>): this {
    this.assertMutable();
    this.#dryRun = body;
    this.#spec.has_dry_run = true;
    return this;
  }
  safeWithoutDryRun(): this {
    this.assertMutable();
    this.#spec.safe_without_dry_run = true;
    return this;
  }
  risk(...labels: readonly string[]): this {
    this.assertMutable();
    this.#spec.risks = names("risk", [...(this.#spec.risks ?? []), ...labels]);
    return this;
  }
  addGroup(id: string): void {
    this.assertMutable();
    this.#spec.groups = unique([...(this.#spec.groups ?? []), id]);
  }
  private assertMutable(): void {
    this.owner.assertMutable();
  }
  get isResult(): boolean {
    return this.#result;
  }
  get dryRunBody(): API.Body<T> | undefined {
    return this.#dryRun;
  }
  closures(): Closure[] {
    return this.#skip.map((fn, i) => ({
      id: `${encodeURIComponent(this.owner.id)}/step/${encodeURIComponent(this.id)}/skip_if/${i}`,
      kind: "skip_if",
      fn,
      job: this.owner,
      step: this,
    }));
  }
  toSpec(): StepSpec {
    const spec = structuredClone(this.#spec);
    optionalField(spec, "closures", collect(this.closures()));
    return spec;
  }
}

class StepGroup implements API.StepGroup {
  readonly id: string;
  readonly members: readonly Step<unknown>[];
  constructor(id: string, members: readonly Step<unknown>[]) {
    this.id = id;
    this.members = Object.freeze([...members]);
  }
  needs(...steps: readonly (API.Step<unknown> | API.StepGroup | string)[]): this {
    for (const member of this.members) member.needs(...steps);
    return this;
  }
  skipIf(fn: API.Predicate): this {
    for (const member of this.members) member.skipIf(fn);
    return this;
  }
}

export class Declaration {
  readonly id: string;
  readonly owner: Plan;
  protected spec: JobSpec;
  protected callbacks: Closure[] = [];
  constructor(owner: Plan, id: string) {
    identifier("job", id);
    this.owner = owner;
    this.id = id;
    this.spec = { id };
  }
  needs(...jobs: readonly API.Dependency[]): this {
    this.assertMutable();
    this.spec.deps = unique([...(this.spec.deps ?? []), ...dependencyIDs(jobs)]);
    return this;
  }
  needsIfPresent(...jobs: readonly API.Dependency[]): this {
    this.assertMutable();
    this.spec.deps_if_present = unique([...(this.spec.deps_if_present ?? []), ...dependencyIDs(jobs)]);
    return this;
  }
  needsGenerated(...generators: readonly API.Generator<unknown>[]): this {
    this.assertMutable();
    this.spec.needs_generated = unique([...(this.spec.needs_generated ?? []), ...generators.map((g) => g.id)]);
    return this;
  }
  skipIf(fn: API.Predicate, options: API.SkipOptions = {}): this {
    this.assertMutable();
    this.callbacks.push({ id: this.closureID("skip_if"), kind: "skip_if", fn, job: this });
    if (options.budgetMs !== undefined) this.spec.skip_if_budget_ms = integer("skip budget", options.budgetMs);
    return this;
  }
  optional(): this {
    this.assertMutable();
    this.spec.optional = true;
    return this.continueOnError();
  }
  continueOnError(): this {
    this.assertMutable();
    this.spec.continue_on_error = true;
    return this;
  }
  onFailure<T>(id: string, body: API.Body<T>): Job<T> {
    this.assertMutable();
    if (this.owner.jobs().some((job) => job.spec.on_failure_of === this.id)) throw new Error("job already has a recovery");
    const recovery = this.owner.job(id, body);
    recovery.spec.on_failure_of = this.id;
    return recovery;
  }
  addGroup(id: string): void {
    this.assertMutable();
    this.spec.groups = unique([...(this.spec.groups ?? []), id]);
  }
  assertMutable(): void {
    this.owner.assertMutable();
  }
  protected closureID(kind: Closure["kind"]): string {
    return `${encodeURIComponent(this.id)}/${kind}/${this.callbacks.filter((c) => c.kind === kind).length}`;
  }
  steps(): readonly Step<unknown>[] {
    return [];
  }
  findStep(id: string): Step<unknown> | undefined {
    return this.steps().find((step) => step.id === id);
  }
  argSpecs(): readonly API.ArgSpec[] {
    return [];
  }
  closures(): Closure[] {
    return [...this.callbacks, ...this.steps().flatMap((step) => step.closures())];
  }
  toSpec(): JobSpec {
    const spec = structuredClone(this.spec);
    if (this.argSpecs().length) spec.arguments = this.argSpecs().map((arg) => argument(arg, this.id));
    optionalField(spec, "closures", collect(this.callbacks));
    const steps = this.steps();
    if (steps.length) {
      spec.steps = steps.map((step) => step.toSpec());
      const results = steps.filter((step) => step.isResult);
      if (results.length > 1) throw new Error("job has more than one result step");
      if (results[0]) spec.result_step = results[0].id;
    }
    return spec;
  }
}

class Approval extends Declaration implements API.Approval {
  constructor(owner: Plan, id: string, options: API.ApprovalOptions) {
    super(owner, id);
    this.spec.approval = {};
    if (options.message !== undefined) this.spec.approval.message = options.message;
    if (options.timeoutMs !== undefined) this.spec.approval.timeout_ms = integer("approval timeout", options.timeoutMs);
    if (options.onTimeout !== undefined) {
      if (!["fail", "deny", "approve"].includes(options.onTimeout)) throw new Error("invalid approval onTimeout");
      this.spec.approval.on_timeout = options.onTimeout;
    }
  }
}

const knownTools = new Set([
  "aws",
  "buildx",
  "crane",
  "docker",
  "git",
  "go",
  "golangci-lint",
  "helm",
  "kubectl",
  "node",
  "npm",
  "shellcheck",
  "terraform",
]);

export class Job<T = unknown> extends Declaration implements API.Job<T> {
  #steps: Step<unknown>[] = [];
  #args: API.ArgSpec[] = [];
  #tools: string[] = [];
  #requires: string[] = [];
  #hasBody: boolean;
  #stepGroups = new Set<string>();
  constructor(owner: Plan, id: string, body?: API.Body<T>) {
    super(owner, id);
    this.#hasBody = body !== undefined;
    if (body) this.#steps.push(new Step(this, "run", body).result());
  }
  override steps(): readonly Step<unknown>[] {
    return this.#steps;
  }
  override argSpecs(): readonly API.ArgSpec[] {
    return structuredClone(this.#args);
  }
  withArgs(...args: readonly API.ArgSpec[]): this {
    this.assertMutable();
    const all = [...this.#args, ...args];
    this.#args = structuredClone(all);
    return this;
  }
  startsChildren(...pipelines: readonly string[]): this {
    return this.startsChildrenIn("", ...pipelines);
  }
  startsChildrenIn(repository: string, ...pipelines: readonly string[]): this {
    this.assertMutable();
    (this.spec.children ??= []).push(...pipelines.map((pipeline) => ({ pipeline, ...(repository ? { repository } : {}) })));
    return this;
  }
  references(...refs: readonly API.PipelineRef<unknown>[]): this {
    this.assertMutable();
    const entries = (this.spec.pipeline_refs ??= []);
    for (const ref of refs)
      if (!entries.some((entry) => entry.pipeline === ref.pipeline && entry.job === ref.jobId))
        entries.push({ pipeline: ref.pipeline, job: ref.jobId, ...(ref.fromAnyRun ? { from_any_run: true } : {}) });
    return this;
  }
  step<U>(id: string, body: API.Body<U>): Step<U> {
    this.assertMutable();
    if (this.#hasBody) throw new Error("a job with a body cannot also have steps");
    if (this.findStep(id)) throw new Error(`duplicate step ${id}`);
    const step = new Step(this, id, body);
    this.#steps.push(step);
    return step;
  }
  groupSteps(id: string, ...steps: readonly API.Step<unknown>[]): StepGroup {
    this.assertMutable();
    identifier("step group", id);
    if (this.#stepGroups.has(id)) throw new Error(`duplicate step group ${id}`);
    const members = steps.map((step) => {
      if (!(step instanceof Step) || step.owner !== this) throw new Error("step group member belongs to another job");
      return step;
    });
    for (const member of members) member.addGroup(id);
    this.#stepGroups.add(id);
    return new StepGroup(id, members);
  }
  failurePolicy(value: API.FailurePolicy): this {
    this.assertMutable();
    if (!["fail_fast", "collect_all"].includes(value)) throw new Error("invalid failure policy");
    this.spec.failure_policy = value;
    return this;
  }
  retry(attempts: number, options: API.RetryOptions = {}): this {
    this.assertMutable();
    const retry: NonNullable<JobSpec["retry"]> = { attempts: integer("retry attempts", attempts) };
    if (options.backoffMs !== undefined) retry.backoff_ms = integer("retry backoff", options.backoffMs);
    this.spec.retry = retry;
    return this;
  }
  timeout(ms: number): this {
    this.assertMutable();
    this.spec.timeout_ms = integer("timeout", ms);
    return this;
  }
  noProgressTimeout(ms: number): this {
    this.assertMutable();
    this.spec.no_progress_timeout_ms = integer("no-progress timeout", ms);
    return this;
  }
  beforeRun(fn: API.Hook): this {
    this.assertMutable();
    this.callbacks.push({ id: this.closureID("before_run"), kind: "before_run", fn, job: this });
    return this;
  }
  afterRun(fn: API.AfterRunFn): this {
    this.assertMutable();
    this.callbacks.push({ id: this.closureID("after_run"), kind: "after_run", fn, job: this });
    return this;
  }
  verify(fn: API.Hook): this {
    this.assertMutable();
    this.callbacks = this.callbacks.filter((c) => c.kind !== "verify");
    this.callbacks.push({ id: this.closureID("verify"), kind: "verify", fn, job: this });
    return this;
  }
  memo(fn: API.MemoInput | API.MemoKeyFn, options: API.MemoOptions = {}): this {
    this.assertMutable();
    const memo: NonNullable<JobSpec["memo"]> = {};
    if (options.ttlMs !== undefined) memo.ttl_ms = integer("memo TTL", options.ttlMs);
    this.spec.memo = memo;
    this.callbacks = this.callbacks.filter((c) => c.kind !== "memo_key");
    if (typeof fn === "function") this.callbacks.push({ id: this.closureID("memo_key"), kind: "memo_key", fn, job: this });
    else memo.input = jsonValue(fn, true) as API.MemoInput;
    return this;
  }
  cache(...caches: readonly API.CacheSpec[]): this {
    this.assertMutable();
    const entries = caches.map((cache) => {
      if (cache.kind === "go_modules") return { name: "go-modules", resolver: "go-modules", key_files: ["go.sum"] };
      if (cache.kind === "npm") return { name: "npm", resolver: "npm", key_files: ["package-lock.json"] };
      if (cache.kind !== "dir" || !cache.path || !cache.keyFiles?.length)
        throw new Error("directory cache needs a path and at least one key file");
      const clean = posix.normalize(cache.path).replace(/(.)\/+$/, "$1");
      return { name: posix.basename(clean), path: cache.path, key_scope: clean, key_files: names("cache key file", cache.keyFiles) };
    });
    (this.spec.caches ??= []).push(...entries);
    return this;
  }
  artifacts(...globs: readonly string[]): this {
    this.assertMutable();
    this.spec.artifacts = names("artifact glob", [...(this.spec.artifacts ?? []), ...globs]);
    return this;
  }
  consumes(producer: API.Dependency, options: { into?: string } = {}): this {
    this.assertMutable();
    const ids = dependencyIDs([producer]);
    (this.spec.consumes ??= []).push(
      ...ids.map((id) => (options.into === undefined ? { producer: id } : { producer: id, into: options.into })),
    );
    return this.needs(...ids);
  }
  concurrency(group: API.ConcurrencyGroup, cost?: number): this {
    this.assertMutable();
    addConcurrency(this.spec, group, cost);
    return this;
  }
  needsTools(...values: readonly string[]): this {
    this.assertMutable();
    for (const name of values) if (!knownTools.has(name)) throw new Error(`not a known tool: ${name}`);
    this.#tools = unique([...this.#tools, ...values.map((name) => `tool:${name}`)]);
    return this;
  }
  requires(...labels: readonly string[]): this {
    this.assertMutable();
    this.#requires = [...labels];
    return this;
  }
  prefers(...labels: readonly string[]): this {
    this.assertMutable();
    this.spec.prefers = [...labels];
    return this;
  }
  inline(): this {
    this.assertMutable();
    this.spec.inline = true;
    return this;
  }
  env(name: string, value: string, option?: API.EnvOption): this {
    this.assertMutable();
    if (!name || name.includes("=") || name.includes("\0") || value.includes("\0")) throw new Error("invalid environment variable");
    (this.spec.env ??= Object.create(null) as NonNullable<JobSpec["env"]>)[name] =
      option?.phase === "startup" ? { value, startup: true } : { value };
    return this;
  }
  generate(fn: API.GenerateFn<T>): Generator<T> {
    this.assertMutable();
    const generator = new Generator(this.closureID("generate"), this);
    this.callbacks.push({
      id: generator.id,
      kind: "generate",
      fn: fn as API.GenerateFn<unknown>,
      generator: generator as Generator<unknown>,
      job: this,
    });
    return generator;
  }
  override toSpec(): JobSpec {
    const spec = super.toSpec();
    const requires = [...this.#requires, ...this.#tools];
    if (requires.length) spec.requires = requires;
    return spec;
  }
}

export class Generator<T = unknown> implements API.Generator<T> {
  readonly id: string;
  readonly source: Job<T>;
  constructor(id: string, source: Job<T>) {
    this.id = id;
    this.source = source;
  }
}

class JobGroup implements API.JobGroup {
  readonly id: string;
  readonly members: readonly Job<unknown>[];
  constructor(id: string, members: readonly Job<unknown>[]) {
    this.id = id;
    this.members = Object.freeze([...members]);
  }
  private apply(fn: (job: Job<unknown>) => void): this {
    for (const member of this.members) fn(member);
    return this;
  }
  needs(...jobs: readonly API.Dependency[]): this {
    return this.apply((job) => job.needs(...jobs));
  }
  needsIfPresent(...jobs: readonly API.Dependency[]): this {
    return this.apply((job) => job.needsIfPresent(...jobs));
  }
  needsGenerated(...generators: readonly API.Generator<unknown>[]): this {
    return this.apply((job) => job.needsGenerated(...generators));
  }
  retry(attempts: number, options?: API.RetryOptions): this {
    return this.apply((job) => job.retry(attempts, options));
  }
  timeout(ms: number): this {
    return this.apply((job) => job.timeout(ms));
  }
  noProgressTimeout(ms: number): this {
    return this.apply((job) => job.noProgressTimeout(ms));
  }
  skipIf(fn: API.Predicate, options?: API.SkipOptions): this {
    return this.apply((job) => job.skipIf(fn, options));
  }
  beforeRun(fn: API.Hook): this {
    return this.apply((job) => job.beforeRun(fn));
  }
  verify(fn: API.Hook): this {
    return this.apply((job) => job.verify(fn));
  }
  afterRun(fn: API.AfterRunFn): this {
    return this.apply((job) => job.afterRun(fn));
  }
  memo(fn: API.MemoInput | API.MemoKeyFn, options?: API.MemoOptions): this {
    return this.apply((job) => job.memo(fn, options));
  }
  cache(...caches: readonly API.CacheSpec[]): this {
    return this.apply((job) => job.cache(...caches));
  }
  artifacts(...globs: readonly string[]): this {
    return this.apply((job) => job.artifacts(...globs));
  }
  consumes(producer: API.Job<unknown>, options?: { into?: string }): this {
    return this.apply((job) => job.consumes(producer, options));
  }
  concurrency(group: API.ConcurrencyGroup, cost?: number): this {
    return this.apply((job) => job.concurrency(group, cost));
  }
  needsTools(...names: readonly string[]): this {
    return this.apply((job) => job.needsTools(...names));
  }
  requires(...labels: readonly string[]): this {
    return this.apply((job) => job.requires(...labels));
  }
  prefers(...labels: readonly string[]): this {
    return this.apply((job) => job.prefers(...labels));
  }
  inline(): this {
    return this.apply((job) => job.inline());
  }
  optional(): this {
    return this.apply((job) => job.optional());
  }
  continueOnError(): this {
    return this.apply((job) => job.continueOnError());
  }
  env(name: string, value: string, option?: API.EnvOption): this {
    return this.apply((job) => job.env(name, value, option));
  }
}

export class Plan implements API.Plan {
  protected declarations: Declaration[] = [];
  protected settings: Omit<PlanDoc, "jobs"> = {};
  #groups = new Set<string>();
  #sealed = false;
  assertMutable(): void {
    if (this.#sealed) throw failure("plan_drift", "declarations cannot change after planning");
  }
  seal(): void {
    this.#sealed = true;
  }
  job<T = unknown>(id: string, body?: API.Body<T>): Job<T> {
    this.assertMutable();
    return this.add(new Job(this, id, body));
  }
  approval(id: string, options: API.ApprovalOptions = {}): Approval {
    this.assertMutable();
    return this.add(new Approval(this, id, options));
  }
  protected add<T extends Declaration>(job: T): T {
    this.assertMutable();
    if (this.findJob(job.id)) throw new Error(`duplicate job ${job.id}`);
    this.declarations.push(job);
    return job;
  }
  groupJobs(id: string, ...jobs: readonly API.Job<unknown>[]): JobGroup {
    this.assertMutable();
    identifier("job group", id);
    if (this.#groups.has(id)) throw new Error(`duplicate job group ${id}`);
    const members = jobs.map((job) => {
      if (!(job instanceof Job) || job.owner !== this) throw new Error("job group member belongs to another plan");
      return job;
    });
    for (const member of members) member.addGroup(id);
    this.#groups.add(id);
    return new JobGroup(id, members);
  }
  concurrency(group: API.ConcurrencyGroup, cost?: number): this {
    this.assertMutable();
    addConcurrency(this.settings, group, cost);
    return this;
  }
  priority(value: number): this {
    this.assertMutable();
    if (!Number.isSafeInteger(value)) throw new Error("priority must be an integer");
    this.settings.priority = value;
    return this;
  }
  claimWait(ms: number): this {
    this.assertMutable();
    this.settings.claim_wait_ms = integer("claim wait", ms);
    return this;
  }
  admissionClass(value: string): this {
    this.assertMutable();
    identifier("admission class", value);
    this.settings.admission_class = value;
    return this;
  }
  checkout(options: API.Checkout): this {
    this.assertMutable();
    const source: NonNullable<PlanDoc["source"]> = {};
    if (options.depth !== undefined) source.depth = integer("checkout depth", options.depth);
    if (options.fullHistory !== undefined) source.full_history = options.fullHistory;
    if (options.tags !== undefined) source.tags = options.tags;
    if (options.submodules !== undefined) source.submodules = options.submodules;
    if (options.lfs !== undefined) source.lfs = options.lfs;
    this.settings.source = source;
    return this;
  }
  jobs(): readonly Declaration[] {
    return this.declarations;
  }
  findJob(id: string): Declaration | undefined {
    return this.declarations.find((job) => job.id === id);
  }
  findClosure(id: string): Closure | undefined {
    return this.declarations.flatMap((job) => job.closures()).find((closure) => closure.id === id);
  }
  toDoc(): PlanDoc {
    const jobs = this.declarations.map((job) => job.toSpec());
    return { jobs, ...structuredClone(this.settings) };
  }
  insert(generated: Generated): JobSpec[] {
    if (generated.parent !== this) throw new Error("generated jobs belong to another plan");
    const jobs = generated.toDoc().jobs;
    generated.seal();
    this.declarations.push(...generated.jobs());
    return jobs;
  }
}

export class Generated extends Plan implements API.Generated {
  readonly parent: Plan;
  readonly source: Job<unknown>;
  readonly generatorID: string;
  constructor(source: Job<unknown>, generatorID: string, parent: Plan) {
    super();
    this.parent = parent;
    this.source = source;
    this.generatorID = generatorID;
    const closure = parent.findClosure(generatorID);
    if (closure?.kind !== "generate" || closure.job !== source) throw new Error("generator does not belong to source");
  }
  protected override add<T extends Declaration>(job: T): T {
    job.needs(this.source).addGroup(this.generatorID);
    for (const group of this.source.toSpec().groups ?? []) job.addGroup(group);
    return super.add(job);
  }
  override toDoc(): PlanDoc {
    if (Object.keys(this.settings).length) throw new Error("generated jobs cannot change plan settings");
    const jobs = this.declarations.map((job) => job.toSpec());
    return { jobs };
  }
}

export function refTo<T>(job: API.Job<T>): API.Ref<T> {
  return {
    jobId: job.id,
    async get(ctx) {
      const value = await ctx.output(job);
      if (value === undefined) throw failure("not_found", "job output not found");
      return value;
    },
    async tryGet(ctx) {
      return tryReference(() => ctx.output(job));
    },
  };
}
export function refToLastRun<T>(pipeline: string, jobId: string, options: API.RefOptions = {}): API.PipelineRef<T> {
  identifier("pipeline", pipeline);
  identifier("job", jobId);
  const ref: API.PipelineRef<T> = {
    pipeline,
    jobId,
    get(ctx) {
      return ctx.latest(ref);
    },
    tryGet(ctx) {
      return tryReference(() => ctx.latest(ref));
    },
    ...(options.fromAnyRun ? { fromAnyRun: true } : {}),
    ...(options.maxAgeMs === undefined ? {} : { maxAgeMs: integer("reference max age", options.maxAgeMs) }),
  };
  return Object.freeze(ref);
}
async function tryReference<T>(read: () => Promise<T | undefined>): Promise<T | undefined> {
  try {
    return await read();
  } catch (error) {
    if (error instanceof SDKError && error.code === "not_found") return undefined;
    throw error;
  }
}

const registry = new Map<string, API.PipelineDef>();
export function definePipeline(definition: API.PipelineDef): API.PipelineDef {
  identifier("pipeline", definition.name);
  if (registry.has(definition.name)) throw new Error(`pipeline ${definition.name} is already defined`);
  registry.set(definition.name, definition);
  return definition;
}
export function pipelines(): API.PipelineDef[] {
  return [...registry.values()].sort((a, b) => a.name.localeCompare(b.name));
}
export function lookupPipeline(name: string): API.PipelineDef | undefined {
  return registry.get(name);
}
export function resetPipelines(): void {
  registry.clear();
}
