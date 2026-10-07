// The plan DSL. A pipeline's plan function declares jobs, their dependencies
// and envelope; the engine reads the resulting plan document and schedules
// it. Bodies and closures stay in this process and run when the engine asks.

import type { LogWriter } from "./log.ts";
import { PROTOCOL_VERSION, type PlanDoc, type PlanNode } from "./describe.ts";

export interface RunContext {
  runId: string;
  pipeline: string;
  args: Record<string, string>;
  git: Record<string, unknown>;
}

export interface NodeContext {
  runId: string;
  nodeId: string;
  attempt: number;
  args: Record<string, string>;
  dryRun: boolean;
  log: LogWriter;
  secret(name: string): Promise<string>;
  output<T = unknown>(nodeId: string): Promise<T>;
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
  readonly skipIfs: Predicate[] = [];
  #deps: string[] = [];
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

  /** Adds a step; the engine runs steps one request at a time in dependency order. */
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

  skipIfIds(): string[] {
    return this.skipIfs.map((_, i) => `${this.id}/skip_if/${i}`);
  }

  toNode(): PlanNode {
    const node: PlanNode = { id: this.id, deps: [...this.#deps] };
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
        steps: this.steps.map((s) => (s.needIds.length > 0 ? { id: s.id, needs: [...s.needIds] } : { id: s.id })),
      };
    }
    if (this.skipIfs.length > 0) node.closures = { skip_if: this.skipIfIds() };
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
    const nodes = this.jobs.map((j) => j.toNode());
    for (const j of this.jobs) {
      if (!j.body && j.steps.length === 0) throw new Error(`job ${j.id} has neither a body nor steps`);
      assertAcyclic(`job ${j.id} steps`, j.steps.map((s) => ({ id: s.id, deps: [...s.needIds] })));
    }
    assertAcyclic("plan", nodes);
    return { protocol: PROTOCOL_VERSION, pipeline, run_id: runId, nodes };
  }
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
