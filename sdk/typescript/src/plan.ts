// The plan DSL. A pipeline's plan function declares jobs, their dependencies
// and envelope; the engine reads the resulting plan document and schedules
// it. Bodies and closures stay in this process and run when the engine asks.

import type { LogWriter } from "./log.ts";
import { PROTOCOL_VERSION, type PlanDoc, type PlanNode } from "./describe.ts";

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

export interface NodeContext {
  runId: string;
  nodeId: string;
  attempt: number;
  args: Record<string, string>;
  dryRun: boolean;
  log: LogWriter;
  /** Aborts when the engine cancels this step or stops asking. */
  signal: AbortSignal;
  secret(name: string): Promise<string>;
  /** Another node's output, read through the engine's output route. */
  output<T = unknown>(nodeId: string): Promise<T | null>;
  /** The output of a step of this node that already ran in this process. */
  stepOutput<T = unknown>(stepId: string): T | undefined;
}

export type Body = (ctx: NodeContext) => unknown;
export type Predicate = (ctx: NodeContext) => boolean | Promise<boolean>;

/** Adds the nodes of a dynamic fan-out from its source node's output. */
export type GenerateFn<T = unknown> = (gen: Generated, output: T | null, ctx: NodeContext) => void | Promise<void>;

export interface RetryOptions {
  backoffMs?: number;
}

const idPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

function checkId(kind: string, id: string): void {
  if (!idPattern.test(id)) throw new Error(`${kind} id ${JSON.stringify(id)} must match ${idPattern}`);
}

export class Step {
  readonly id: string;
  readonly body: Body;
  readonly #needs: string[] = [];
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

  get needIds(): readonly string[] {
    return this.#needs;
  }

  get isResult(): boolean {
    return this.#result;
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
  readonly body: Body | undefined;
  readonly steps: Step[] = [];
  readonly skipIfs: Predicate[] = [];
  readonly generators: Generator[] = [];
  #deps: string[] = [];
  #needsGenerated: string[] = [];
  #retry: { attempts: number; backoffMs?: number } | undefined;
  #timeoutMs: number | undefined;

  constructor(id: string, body?: Body) {
    checkId("job", id);
    this.id = id;
    this.body = body;
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
    if (!Number.isInteger(attempts) || attempts < 0) throw new Error(`job ${this.id}: retry attempts must be a non-negative integer`);
    if (opts.backoffMs !== undefined && (!Number.isInteger(opts.backoffMs) || opts.backoffMs < 0)) {
      throw new Error(`job ${this.id}: retry backoff must be a non-negative integer of milliseconds`);
    }
    this.#retry = opts.backoffMs === undefined ? { attempts } : { attempts, backoffMs: opts.backoffMs };
    return this;
  }

  /** Bounds one attempt's wall time; the engine kills the node process when it passes. */
  timeout(ms: number): this {
    if (!Number.isInteger(ms) || ms <= 0) throw new Error(`job ${this.id}: timeout must be a positive integer of milliseconds`);
    this.#timeoutMs = ms;
    return this;
  }

  /** Adds a step; the engine runs each step with its own request, in dependency order. */
  step(id: string, body: Body): Step {
    if (this.body) throw new Error(`job ${this.id} has a body, so it cannot also have steps`);
    if (this.steps.some((s) => s.id === id)) throw new Error(`job ${this.id}: duplicate step ${id}`);
    const s = new Step(id, body);
    this.steps.push(s);
    return s;
  }

  /** Adds a predicate the engine evaluates by closure id before starting the job; true skips it. */
  skipIf(fn: Predicate): this {
    this.skipIfs.push(fn);
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

  skipIfIds(): string[] {
    return this.skipIfs.map((_, i) => `${this.id}/skip_if/${i}`);
  }

  toNode(): PlanNode {
    const node: PlanNode = { id: this.id, deps: [...this.#deps] };
    if (this.generators.length > 0) node.dynamic = true;
    const m: NonNullable<PlanNode["modifiers"]> = {};
    if (this.#retry) {
      m.retry = this.#retry.attempts;
      if (this.#retry.backoffMs !== undefined) m.retry_backoff_ms = this.#retry.backoffMs;
    }
    if (this.#timeoutMs !== undefined) m.timeout_ms = this.#timeoutMs;
    if (this.skipIfs.length > 0) m.has_skip_if = true;
    if (Object.keys(m).length > 0) node.modifiers = m;
    if (this.steps.length > 0) {
      node.work = {
        steps: this.steps.map((s) => {
          const step: NonNullable<PlanNode["work"]>["steps"][number] = {
            id: s.id,
          };
          if (s.needIds.length > 0) step.needs = [...s.needIds];
          if (s.isResult) step.is_result = true;
          return step;
        }),
      };
      const result = this.steps.find((s) => s.isResult);
      if (result) node.work.result_step = result.id;
    }
    const closures: NonNullable<PlanNode["closures"]> = {};
    if (this.skipIfs.length > 0) closures.skip_if = this.skipIfIds();
    if (this.generators.length > 0) closures.generator = this.generators.map((g) => g.id);
    if (Object.keys(closures).length > 0) node.closures = closures;
    if (this.#needsGenerated.length > 0) node.needs_generated = [...this.#needsGenerated];
    return node;
  }
}

export class Plan {
  readonly jobs: Job[] = [];

  /** Declares a job; pass a body, or none and add steps with job.step(). */
  job(id: string, body?: Body): Job {
    if (this.jobs.some((j) => j.id === id)) throw new Error(`duplicate job ${id}`);
    const j = new Job(id, body);
    this.jobs.push(j);
    return j;
  }

  lookup(id: string): Job | undefined {
    return this.jobs.find((j) => j.id === id);
  }

  /** Validates the graph and returns the plan document the engine schedules. */
  toDoc(pipeline: string, runId: string): PlanDoc {
    const nodes = checkJobs(this.jobs, this.jobs);
    return { protocol: PROTOCOL_VERSION, pipeline, run_id: runId, nodes };
  }

  /**
   * Adds the jobs a generator produced, after checking them against the
   * plan, and returns their node documents.
   */
  insert(added: Job[]): PlanNode[] {
    for (const j of added) {
      if (this.lookup(j.id)) throw new Error(`generated job ${j.id} collides with a job of the plan`);
    }
    const nodes = checkJobs(added, [...this.jobs, ...added]);
    this.jobs.push(...added);
    return nodes;
  }
}

function checkJobs(jobs: Job[], all: Job[]): PlanNode[] {
  for (const j of jobs) {
    if (!j.body && j.steps.length === 0) throw new Error(`job ${j.id} has neither a body nor steps`);
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
