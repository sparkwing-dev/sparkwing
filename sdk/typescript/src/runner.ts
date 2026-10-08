// The node runner loop. The engine starts this process once per node
// attempt, writes requests to stdin, and reads replies and log records from
// stdout. A plan request comes first: bodies and closures are captured while
// the plan function runs, so run_node, run_step and eval use that plan.

import type { Readable } from "node:stream";
import { describe } from "./describe.ts";
import { LogWriter, type LineSink } from "./log.ts";
import { lookupPipeline, Plan, type Job, type NodeContext, type Predicate, type RunContext } from "./plan.ts";
import { isInvalid, readRequests, writeReply, type Request } from "./protocol.ts";
import { NodeRoutes, routeConfigFromEnv } from "./routes.ts";

export interface ServeOptions {
  input: Readable;
  output: LineSink;
  env?: Record<string, string | undefined>;
  routes?: NodeRoutes;
}

export interface NodeResult {
  outcome: "success" | "failed";
  output?: unknown;
  error?: string;
}

interface Planned {
  plan: Plan;
  run: RunContext;
  closures: Map<string, { job: Job; fn: Predicate }>;
}

/**
 * Answers engine requests until stdin ends. Requests other than eval run one
 * at a time in arrival order; an eval waits only for the latest plan, so a
 * closure of a running node is answered while the node's body still runs.
 */
export async function serve(opts: ServeOptions): Promise<void> {
  const session = new Session(opts);
  let serial: Promise<void> = Promise.resolve();
  let planned: Promise<void> = Promise.resolve();
  const evals: Array<Promise<void>> = [];
  for await (const req of readRequests(opts.input)) {
    if (isInvalid(req)) {
      if (req.id !== null) writeReply(opts.output, { reply: req.id, ok: false, error: { message: req.invalid } });
      else new LogWriter(opts.output).error(req.invalid);
      continue;
    }
    if (req.op === "eval") {
      evals.push(planned.then(() => session.answer(req)));
      continue;
    }
    serial = serial.then(() => session.answer(req));
    if (req.op === "plan") planned = serial;
  }
  await Promise.all([serial, ...evals]);
}

class Session {
  readonly #opts: ServeOptions;
  #planned: Planned | undefined;
  #routes: NodeRoutes | undefined;

  constructor(opts: ServeOptions) {
    this.#opts = opts;
    this.#routes = opts.routes;
  }

  async answer(req: Request): Promise<void> {
    try {
      const result = await this.#handle(req);
      writeReply(this.#opts.output, { reply: req.id, ok: true, result: result ?? null });
    } catch (err) {
      writeReply(this.#opts.output, { reply: req.id, ok: false, error: { message: messageOf(err) } });
    }
  }

  async #handle(req: Request): Promise<unknown> {
    switch (req.op) {
      case "describe":
        return describe();
      case "plan":
        return this.#plan(req);
      case "run_node": {
        const job = this.#job(str(req, "node"));
        if (!job.body) throw new Error(`job ${job.id} has steps; the engine runs them with run_step`);
        return this.#invoke(req, job.id, job.body, undefined);
      }
      case "run_step": {
        const job = this.#job(str(req, "node"));
        const stepId = str(req, "step");
        const step = job.steps.find((s) => s.id === stepId);
        if (!step) throw new Error(`job ${job.id} has no step ${stepId}`);
        return this.#invoke(req, job.id, step.body, step.id);
      }
      case "eval": {
        const id = str(req, "closure");
        const closure = this.#current().closures.get(id);
        if (!closure) throw new Error(`no closure ${id} in this plan`);
        return { value: await closure.fn(this.#context(req, closure.job.id, undefined)) };
      }
      default:
        throw new Error(`unknown op ${String(req.op)}`);
    }
  }

  async #plan(req: Request): Promise<unknown> {
    const pipeline = str(req, "pipeline");
    const args = stringMap(req["args"], "args");
    const run = req["run"];
    if (typeof run !== "object" || run === null) throw new Error("run must be an object");
    const { run_id: runId, pipeline: _p, ...git } = run as Record<string, unknown>;
    if (typeof runId !== "string" || runId === "") throw new Error("run.run_id must be a non-empty string");
    const def = lookupPipeline(pipeline);
    if (!def) throw new Error(`no pipeline ${pipeline}`);
    const ctx: RunContext = { runId, pipeline, args, git };
    const plan = new Plan();
    await def.plan(plan, ctx);
    const doc = plan.toDoc(pipeline, runId);
    const closures = new Map<string, { job: Job; fn: Predicate }>();
    for (const job of plan.jobs) {
      job.skipIfIds().forEach((id, i) => closures.set(id, { job, fn: job.skipIfs[i] as Predicate }));
    }
    this.#planned = { plan, run: ctx, closures };
    return doc;
  }

  #current(): Planned {
    if (!this.#planned) throw new Error("no plan in this process yet; the engine sends plan first");
    return this.#planned;
  }

  #job(nodeId: string): Job {
    const job = this.#current().plan.lookup(nodeId);
    if (!job) throw new Error(`pipeline ${this.#current().run.pipeline} has no job ${nodeId}`);
    return job;
  }

  async #invoke(req: Request, nodeId: string, body: (ctx: NodeContext) => unknown, step: string | undefined): Promise<NodeResult> {
    const ctx = this.#context(req, nodeId, step);
    try {
      const output = await body(ctx);
      return output === undefined ? { outcome: "success" } : { outcome: "success", output };
    } catch (err) {
      const message = messageOf(err);
      ctx.log.error(message);
      return { outcome: "failed", error: message };
    }
  }

  #context(req: Request, nodeId: string, step: string | undefined): NodeContext {
    const { run } = this.#current();
    const nodeLog = new LogWriter(this.#opts.output, { node: nodeId });
    const options = req["options"];
    const attempt = req["attempt"];
    return {
      runId: run.runId,
      nodeId,
      attempt: typeof attempt === "number" ? attempt : 1,
      args: run.args,
      dryRun: typeof options === "object" && options !== null && (options as { dry_run?: unknown }).dry_run === true,
      log: step === undefined ? nodeLog : nodeLog.forStep(step),
      secret: (name) => this.#nodeRoutes().secret(name),
      output: (id) => this.#nodeRoutes().output(id),
    };
  }

  #nodeRoutes(): NodeRoutes {
    this.#routes ??= new NodeRoutes(routeConfigFromEnv(this.#opts.env ?? process.env));
    return this.#routes;
  }
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function str(req: Request, key: string): string {
  const v = req[key];
  if (typeof v !== "string" || v === "") throw new Error(`${key} must be a non-empty string`);
  return v;
}

function stringMap(v: unknown, key: string): Record<string, string> {
  if (v === undefined) return {};
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error(`${key} must be an object of strings`);
  for (const [k, s] of Object.entries(v)) {
    if (typeof s !== "string") throw new Error(`${key}.${k} must be a string`);
  }
  return v as Record<string, string>;
}
