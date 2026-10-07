// The node runner loop. The engine starts this process, writes requests to
// its stdin and reads responses and log records from its stdout. Plans are
// rebuilt from (pipeline, args) in each process, because bodies and closures
// are captured while the plan function runs.

import type { Readable } from "node:stream";
import { describe } from "./describe.ts";
import { LogWriter, type LineSink } from "./log.ts";
import { closureId, lookupPipeline, Plan, type Job, type NodeContext } from "./plan.ts";
import { readRequests, writeResponse, type ProtocolError, type Request } from "./protocol.ts";
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

class Refusal extends Error {
  readonly code: ProtocolError["code"];

  constructor(code: ProtocolError["code"], message: string) {
    super(message);
    this.code = code;
  }
}

interface RunParams {
  pipeline: string;
  runId: string;
  args: Record<string, string>;
}

/** Answers engine requests until shutdown or end of input. */
export async function serve(opts: ServeOptions): Promise<void> {
  const session = new Session(opts);
  for await (const req of readRequests(opts.input)) {
    if ("invalid" in req) {
      if (req.id !== null) writeResponse(opts.output, { id: req.id, error: { code: "bad_request", message: req.invalid } });
      else new LogWriter(opts.output).error(req.invalid);
      continue;
    }
    try {
      const result = await session.handle(req);
      writeResponse(opts.output, { id: req.id, result: result ?? null });
    } catch (err) {
      const code = err instanceof Refusal ? err.code : "internal";
      writeResponse(opts.output, { id: req.id, error: { code, message: (err as Error).message } });
    }
    if (req.method === "shutdown") break;
  }
  session.abort.abort();
}

class Session {
  readonly abort = new AbortController();
  readonly #opts: ServeOptions;
  readonly #plans = new Map<string, Plan>();
  #routes: NodeRoutes | undefined;

  constructor(opts: ServeOptions) {
    this.#opts = opts;
    this.#routes = opts.routes;
  }

  async handle(req: Request): Promise<unknown> {
    const p = req.params ?? {};
    switch (req.method) {
      case "describe":
        return describe();
      case "plan": {
        const run = runParams(p);
        return (await this.#plan(run)).toDoc(run.pipeline);
      }
      case "run_node": {
        const run = runParams(p);
        const job = await this.#job(run, str(p, "node"));
        if (!job.body) throw new Refusal("bad_request", `job ${job.id} has steps; run them with run_step`);
        return this.#invoke(run, job.id, job.body, undefined);
      }
      case "run_step": {
        const run = runParams(p);
        const job = await this.#job(run, str(p, "node"));
        const stepId = str(p, "step");
        const step = job.steps.find((s) => s.id === stepId);
        if (!step) throw new Refusal("unknown_step", `job ${job.id} has no step ${stepId}`);
        return this.#invoke(run, job.id, step.body, step.id);
      }
      case "call": {
        const run = runParams(p);
        const id = str(p, "closure");
        const [jobId, kind] = id.split("/");
        const job = jobId ? (await this.#plan(run)).lookup(jobId) : undefined;
        if (!job || kind !== "skip_if" || !job.skipIfFn || closureId(job.id, kind) !== id) {
          throw new Refusal("unknown_closure", `no closure ${id}`);
        }
        return { value: await job.skipIfFn(this.#context(run, job.id, undefined)) };
      }
      case "shutdown":
        return null;
      default:
        throw new Refusal("unknown_method", `unknown method ${String((req as { method: unknown }).method)}`);
    }
  }

  async #plan(run: RunParams): Promise<Plan> {
    const key = JSON.stringify([run.pipeline, run.runId, Object.entries(run.args).sort()]);
    const cached = this.#plans.get(key);
    if (cached) return cached;
    const def = lookupPipeline(run.pipeline);
    if (!def) throw new Refusal("unknown_pipeline", `no pipeline ${run.pipeline}`);
    const plan = new Plan();
    await def.plan(plan, { runId: run.runId, pipeline: run.pipeline, args: run.args });
    plan.toDoc(run.pipeline);
    this.#plans.set(key, plan);
    return plan;
  }

  async #job(run: RunParams, nodeId: string): Promise<Job> {
    const job = (await this.#plan(run)).lookup(nodeId);
    if (!job) throw new Refusal("unknown_node", `pipeline ${run.pipeline} has no job ${nodeId}`);
    return job;
  }

  async #invoke(run: RunParams, nodeId: string, body: (ctx: NodeContext) => unknown, step: string | undefined): Promise<NodeResult> {
    const ctx = this.#context(run, nodeId, step);
    try {
      const output = await body(ctx);
      return output === undefined ? { outcome: "success" } : { outcome: "success", output };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      ctx.log.error(message);
      return { outcome: "failed", error: message };
    }
  }

  #context(run: RunParams, nodeId: string, step: string | undefined): NodeContext {
    const nodeLog = new LogWriter(this.#opts.output, { node: nodeId });
    return {
      runId: run.runId,
      nodeId,
      args: run.args,
      log: step === undefined ? nodeLog : nodeLog.forStep(step),
      secret: (name) => this.#nodeRoutes().secret(name),
      output: (id) => this.#nodeRoutes().output(id),
      signal: this.abort.signal,
    };
  }

  #nodeRoutes(): NodeRoutes {
    this.#routes ??= new NodeRoutes(routeConfigFromEnv(this.#opts.env ?? process.env));
    return this.#routes;
  }
}

function str(params: Record<string, unknown>, key: string): string {
  const v = params[key];
  if (typeof v !== "string" || v === "") throw new Refusal("bad_request", `params.${key} must be a non-empty string`);
  return v;
}

function runParams(params: Record<string, unknown>): RunParams {
  const args = params["args"] ?? {};
  if (typeof args !== "object" || args === null || Array.isArray(args)) {
    throw new Refusal("bad_request", "params.args must be an object of strings");
  }
  for (const [k, v] of Object.entries(args)) {
    if (typeof v !== "string") throw new Refusal("bad_request", `params.args.${k} must be a string`);
  }
  return { pipeline: str(params, "pipeline"), runId: str(params, "run_id"), args: args as Record<string, string> };
}
