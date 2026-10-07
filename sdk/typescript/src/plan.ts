// The plan DSL. A pipeline's plan function declares jobs, their dependencies
// and envelope; the engine reads the resulting plan document and schedules
// it. Bodies and closures stay in this process and run when the engine asks.

import type { LogWriter } from "./log.ts";
import type { PlanDoc, PlanNode } from "./describe.ts";

export interface RunContext {
  runId: string;
  pipeline: string;
  args: Record<string, string>;
}

export interface NodeContext {
  runId: string;
  nodeId: string;
  args: Record<string, string>;
  log: LogWriter;
  secret(name: string): Promise<string>;
  output<T = unknown>(nodeId: string): Promise<T>;
  signal: AbortSignal;
}

export type Body = (ctx: NodeContext) => unknown;
export type Predicate = (ctx: NodeContext) => boolean | Promise<boolean>;

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

  constructor(id: string, body: Body) {
    checkId("step", id);
    this.id = id;
    this.body = body;
  }

  needs(...steps: Array<Step | string>): this {
    for (const s of steps) this.#needs.push(typeof s === "string" ? s : s.id);
    return this;
  }

  get needIds(): readonly string[] {
    return this.#needs;
  }
}

export class Job {
  readonly id: string;
  readonly body: Body | undefined;
  readonly steps: Step[] = [];
  skipIfFn: Predicate | undefined;
  #needs: string[] = [];
  #retry: { attempts: number; backoffMs?: number } | undefined;
  #timeoutMs: number | undefined;

  constructor(id: string, body?: Body) {
    checkId("job", id);
    this.id = id;
    this.body = body;
  }

  /** Runs this job only after every named job succeeds. */
  needs(...jobs: Array<Job | string>): this {
    for (const j of jobs) this.#needs.push(typeof j === "string" ? j : j.id);
    return this;
  }

  /** Lets the engine start the job again, up to attempts more times, after a failure. */
  retry(attempts: number, opts: RetryOptions = {}): this {
    if (!Number.isInteger(attempts) || attempts < 0) throw new Error(`job ${this.id}: retry attempts must be a non-negative integer`);
    this.#retry = opts.backoffMs === undefined ? { attempts } : { attempts, backoffMs: opts.backoffMs };
    return this;
  }

  /** Bounds one attempt's wall time; the engine kills the node process when it passes. */
  timeout(ms: number): this {
    if (!Number.isFinite(ms) || ms <= 0) throw new Error(`job ${this.id}: timeout must be a positive number of milliseconds`);
    this.#timeoutMs = ms;
    return this;
  }

  /** Adds a step; the engine runs steps one request at a time in dependency order. */
  step(id: string, body: Body): Step {
    if (this.body) throw new Error(`job ${this.id} has a body, so it cannot also have steps`);
    if (this.steps.some((s) => s.id === id)) throw new Error(`job ${this.id}: duplicate step ${id}`);
    const s = new Step(id, body);
    this.steps.push(s);
    return s;
  }

  /** Registers a closure the engine calls by id before starting the job. */
  skipIf(fn: Predicate): this {
    this.skipIfFn = fn;
    return this;
  }

  toNode(): PlanNode {
    const node: PlanNode = { id: this.id, needs: [...this.#needs] };
    if (this.#retry) {
      node.retry = this.#retry.backoffMs === undefined
        ? { attempts: this.#retry.attempts }
        : { attempts: this.#retry.attempts, backoff_ms: this.#retry.backoffMs };
    }
    if (this.#timeoutMs !== undefined) node.timeout_ms = this.#timeoutMs;
    if (this.steps.length > 0) node.steps = this.steps.map((s) => ({ id: s.id, needs: [...s.needIds] }));
    if (this.skipIfFn) node.closures = { skip_if: closureId(this.id, "skip_if") };
    return node;
  }
}

export function closureId(jobId: string, kind: string): string {
  return `${jobId}/${kind}`;
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
  toDoc(pipeline: string): PlanDoc {
    const nodes = this.jobs.map((j) => j.toNode());
    for (const j of this.jobs) {
      if (!j.body && j.steps.length === 0) throw new Error(`job ${j.id} has neither a body nor steps`);
      assertAcyclic(`job ${j.id} steps`, j.steps.map((s) => ({ id: s.id, needs: [...s.needIds] })));
    }
    assertAcyclic("plan", nodes);
    return { pipeline, nodes };
  }
}

function assertAcyclic(what: string, nodes: Array<{ id: string; needs: string[] }>): void {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  for (const n of nodes) {
    for (const d of n.needs) {
      if (!byId.has(d)) throw new Error(`${what}: ${n.id} needs ${d}, which does not exist`);
    }
  }
  const state = new Map<string, "visiting" | "done">();
  const visit = (id: string, path: string[]): void => {
    if (state.get(id) === "done") return;
    if (state.get(id) === "visiting") throw new Error(`${what}: cycle ${[...path, id].join(" -> ")}`);
    state.set(id, "visiting");
    for (const d of byId.get(id)?.needs ?? []) visit(d, [...path, id]);
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
  secret?: boolean;
}

export interface PipelineDef {
  name: string;
  short?: string;
  help?: string;
  args?: ArgSpec[];
  plan(plan: Plan, run: RunContext): void | Promise<void>;
}

const registry = new Map<string, PipelineDef>();

/** Registers a pipeline with this process; describe and serve answer for it. */
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
