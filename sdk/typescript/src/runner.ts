// The node runner loop. The engine starts this process with
// --sw-node-protocol, writes requests to the request pipe, and reads replies
// and log records from the frame pipe. A plan request comes first: bodies and
// closures are captured while the plan function runs, so run_node, run_step
// and eval use that plan.

import type { Readable } from "node:stream";
import { describe } from "./describe.ts";
import { LogWriter, type LineSink } from "./log.ts";
import {
  Generated,
  lookupPipeline,
  Plan,
  resolveArgs,
  type Closure,
  type Failure,
  type Generator,
  type GitInfo,
  type Job,
  type NodeContext,
  type RunContext,
  type Step,
  type Trigger,
  withRecoveries,
} from "./plan.ts";
import { MAX_OUTPUT_BYTES, OPS, ProtocolError, readRequests, writeReply, type Request } from "./protocol.ts";
import type { NodeRoutes } from "./routes.ts";

export interface ServeOptions {
  /** The request pipe. */
  input: Readable;
  /** The frame pipe: replies and log records. */
  output: LineSink;
  /** The node routes; absent for a process the engine bound to no node. */
  routes?: NodeRoutes;
}

export interface NodeResult {
  outcome: "success" | "failed";
  output?: unknown;
  error?: string;
}

interface EvalResult {
  value: unknown;
  error?: string;
}

interface Planned {
  plan: Plan;
  run: RunContext;
  closures: Map<string, Closure>;
}

/**
 * Answers engine requests until the request pipe ends. Every request runs as
 * soon as it arrives, since the engine may have several eval and run_step
 * requests in flight. End of input aborts whatever still runs and silences
 * the frame pipe, so serve returns without waiting for those requests.
 */
export async function serve(opts: ServeOptions): Promise<void> {
  const session = new Session(opts);
  try {
    for await (const req of readRequests(opts.input)) {
      void session.answer(req);
    }
  } finally {
    session.close();
  }
}

const needsPlan = new Set(["run_node", "run_step", "eval"]);

class Session {
  readonly #frames: LineSink;
  readonly #routes: NodeRoutes | undefined;
  #done = false;
  #planSeen = false;
  #planned: Planned | undefined;
  readonly #controllers = new Map<string, AbortController>();
  readonly #stepOutputs = new Map<string, Map<string, unknown>>();
  readonly #nodeOutputs = new Map<string, unknown>();
  readonly #generated = new Set<string>();

  constructor(opts: ServeOptions) {
    // safety: after end of input the SDK writes nothing further, so every
    // frame goes through this check, log records included.
    this.#frames = {
      write: (line: string) => (this.#done ? undefined : opts.output.write(line)),
    };
    this.#routes = opts.routes;
  }

  close(): void {
    this.#done = true;
    for (const c of this.#controllers.values()) c.abort();
  }

  async answer(req: Request | { id: string; error: ProtocolError }): Promise<void> {
    try {
      if ("error" in req && req.error instanceof ProtocolError) throw req.error;
      const result = await this.#handle(req as Request);
      writeReply(this.#frames, { reply: req.id, ok: true, result });
    } catch (err) {
      const error = err instanceof ProtocolError ? { code: err.code, message: err.message } : { message: messageOf(err) };
      writeReply(this.#frames, { reply: req.id, ok: false, error });
    }
  }

  async #handle(req: Request): Promise<unknown> {
    if (!(OPS as readonly string[]).includes(req.op)) throw new ProtocolError("unknown_op", `unknown op ${JSON.stringify(req.op)}`);
    if (needsPlan.has(req.op) && !this.#planned) throw new ProtocolError("no_plan", `${req.op} needs a plan earlier in this process`);
    switch (req.op) {
      case "describe":
        return describe();
      case "plan":
        return this.#plan(req);
      case "run_node": {
        // hack: a body is the node's one step, so run_node runs it as the Go
        // SDK runs a node's work; a node with several steps runs each with run_step.
        const job = this.#job(req);
        const [only, ...more] = job.steps;
        if (!only || more.length > 0) {
          throw new ProtocolError("bad_request", `node ${JSON.stringify(job.id)} has no single body to run; run each step with run_step`);
        }
        return this.#runStep(req, job, only);
      }
      case "run_step": {
        const job = this.#job(req);
        const stepId = str(req, "step");
        const step = job.steps.find((s) => s.id === stepId);
        if (!step) throw new ProtocolError("unknown_step", `node ${JSON.stringify(job.id)} has no step ${JSON.stringify(stepId)}`);
        return this.#runStep(req, job, step);
      }
      case "cancel":
        this.#controller(`${str(req, "node")}\u0000${str(req, "step")}`).abort();
        return {};
      case "eval":
        return this.#eval(req);
    }
    throw new ProtocolError("unknown_op", `unknown op ${JSON.stringify(req.op)}`);
  }

  async #runStep(req: Request, job: Job, step: Step): Promise<NodeResult> {
    const body = this.#current().run.dryRun && step.dryRunBody ? step.dryRunBody : step.body;
    const result = await this.#invoke(req, job, step.id, body);
    if (result.outcome === "success") {
      const outputs = this.#stepOutputs.get(job.id) ?? new Map<string, unknown>();
      outputs.set(step.id, result.output);
      this.#stepOutputs.set(job.id, outputs);
    }
    // safety: only the result step's output is the node's output; the host
    // records whatever a run_step reply carries.
    if (!step.isResult) delete result.output;
    if (result.outcome === "success" && (step.isResult || !job.steps.some((s) => s.isResult))) {
      // safety: a node with no result step has no output, and its own
      // generator reads that null here rather than asking the host for a
      // node still running.
      this.#nodeOutputs.set(job.id, result.output ?? null);
    }
    return result;
  }

  async #plan(req: Request): Promise<unknown> {
    if (this.#planSeen) throw new ProtocolError("bad_request", "plan arrives at most once per process");
    this.#planSeen = true;
    const pipeline = str(req, "pipeline");
    const rawArgs = stringMap(req["args"], "args");
    const run = runContext(req["run"]);
    const def = lookupPipeline(pipeline);
    if (!def) throw new ProtocolError("unknown_pipeline", `pipeline ${JSON.stringify(pipeline)} is not registered`);
    const args = resolveArgs(def, rawArgs);
    if (typeof args === "string") throw new ProtocolError("invalid_args", args);
    const ctx: RunContext = {
      ...run,
      pipeline,
      args,
      log: new LogWriter(this.#frames),
    };
    const plan = new Plan();
    let doc;
    try {
      await def.plan(plan, ctx);
      doc = plan.toDoc(pipeline, run.runId);
    } catch (err) {
      throw new ProtocolError("plan_failed", messageOf(err));
    }
    const closures = new Map<string, Closure>();
    this.#planned = { plan, run: ctx, closures };
    this.#register(plan.allJobs());
    return doc;
  }

  #register(jobs: Job[]): void {
    const { closures } = this.#current();
    for (const job of jobs) {
      for (const c of job.closures()) closures.set(c.id, c);
    }
  }

  async #eval(req: Request): Promise<EvalResult> {
    const id = str(req, "closure");
    const closure = this.#current().closures.get(id);
    if (!closure) throw new ProtocolError("unknown_closure", `closure ${JSON.stringify(id)} is not in this process's plan`);
    if (closure.kind === "generator") return this.#expand(req, closure.gen);
    const input = closure.kind === "after_run" ? afterRunError(req["input"]) : undefined;
    const ctx = this.#context(req, closure.job, closure.step?.id);
    try {
      return { value: await callClosure(closure, ctx, input) };
    } catch (err) {
      return { value: null, error: messageOf(err) };
    } finally {
      this.#release(req);
    }
  }

  async #expand(req: Request, gen: Generator): Promise<EvalResult> {
    if (this.#generated.has(gen.id))
      throw new ProtocolError("bad_request", `generator ${JSON.stringify(gen.id)} already ran in this process`);
    this.#generated.add(gen.id);
    const ctx = this.#context(req, gen.source, undefined);
    try {
      // safety: the source's own attempt process holds its output; any other
      // process reads the output the host recorded before it asked.
      const output = this.#nodeOutputs.has(gen.source.id) ? this.#nodeOutputs.get(gen.source.id) : await ctx.output(gen.source.id);
      const added = new Generated(gen.source);
      await gen.fn(added, output as never, ctx);
      const nodes = this.#current().plan.insert(added.jobs);
      this.#register(withRecoveries(added.jobs));
      return { value: nodes };
    } catch (err) {
      return { value: null, error: messageOf(err) };
    } finally {
      this.#release(req);
    }
  }

  #current(): Planned {
    if (!this.#planned) throw new ProtocolError("no_plan", "this request needs a plan earlier in this process");
    return this.#planned;
  }

  #job(req: Request): Job {
    const id = str(req, "node");
    const job = this.#current().plan.lookup(id);
    if (!job) throw new ProtocolError("unknown_node", `node ${JSON.stringify(id)} is not in this process's plan`);
    return job;
  }

  async #invoke(req: Request, job: Job, step: string | undefined, body: (ctx: NodeContext) => unknown): Promise<NodeResult> {
    const attempt = req["attempt"];
    if (typeof attempt !== "number" || !Number.isInteger(attempt) || attempt < 1) {
      throw new ProtocolError("bad_request", "attempt must be an integer of at least 1");
    }
    const ctx = this.#context(req, job, step, attempt);
    const failure = failureOf(req["failure"]);
    if (failure) ctx.failure = failure;
    try {
      ctx.signal.throwIfAborted();
      const output = await body(ctx);
      if (output === undefined) return { outcome: "success" };
      const size = jsonSize(output);
      if (size > MAX_OUTPUT_BYTES) {
        return { outcome: "failed", error: `step output is ${size} bytes as JSON, over the ${MAX_OUTPUT_BYTES >> 20} MiB limit` };
      }
      return { outcome: "success", output };
    } catch (err) {
      return { outcome: "failed", error: messageOf(err) };
    } finally {
      this.#release(req);
    }
  }

  // safety: a cancel names a node and step, so a step's controller is keyed
  // by both; a cancel read first leaves an aborted controller its run_step
  // then starts with.
  #controllerKey(req: Request): string {
    return req.op === "run_step" ? `${String(req["node"])}\u0000${String(req["step"])}` : `#${req.id}`;
  }

  #controller(key: string): AbortController {
    let c = this.#controllers.get(key);
    if (!c) {
      c = new AbortController();
      if (this.#done) c.abort();
      this.#controllers.set(key, c);
    }
    return c;
  }

  #release(req: Request): void {
    this.#controllers.delete(this.#controllerKey(req));
  }

  #context(req: Request, job: Job, step: string | undefined, attempt?: number): NodeContext {
    const { run } = this.#current();
    const nodeLog = new LogWriter(this.#frames, { node: job.id });
    const signal = this.#controller(this.#controllerKey(req)).signal;
    const routes = (): NodeRoutes => {
      if (!this.#routes) throw new Error("the engine gave this process no node routes");
      return this.#routes;
    };
    return {
      runId: run.runId,
      nodeId: job.id,
      attempt: attempt ?? 1,
      args: run.args,
      dryRun: run.dryRun,
      log: step === undefined ? nodeLog : nodeLog.forStep(step),
      signal,
      secret: (name) => routes().secret(name, signal),
      output: (id) => routes().output(id, signal),
      stepOutput: <T>(id: string) => this.#stepOutputs.get(job.id)?.get(id) as T | undefined,
    };
  }
}

function runContext(v: unknown): Omit<RunContext, "pipeline" | "args" | "log"> {
  if (typeof v !== "object" || v === null || Array.isArray(v)) throw new ProtocolError("bad_request", "run must be an object");
  const run = v as Record<string, unknown>;
  if (typeof run["run_id"] !== "string" || run["run_id"] === "")
    throw new ProtocolError("bad_request", "run.run_id must be a non-empty string");
  const out: Omit<RunContext, "pipeline" | "args" | "log"> = {
    runId: run["run_id"],
    dryRun: run["dry_run"] === true,
    noCache: run["no_cache"] === true,
  };
  if (typeof run["started_at"] === "string") out.startedAt = run["started_at"];
  if (typeof run["trigger"] === "object" && run["trigger"] !== null) out.trigger = run["trigger"] as Trigger;
  if (typeof run["git"] === "object" && run["git"] !== null) out.git = run["git"] as GitInfo;
  return out;
}

// safety: a closure's own throw, or a value of the wrong type, is the closure
// failing, which eval answers in a successful reply.
async function callClosure(c: Exclude<Closure, { kind: "generator" }>, ctx: NodeContext, input: Error | undefined): Promise<unknown> {
  switch (c.kind) {
    case "skip_if": {
      const v: unknown = await c.fn(ctx);
      if (typeof v !== "boolean") throw new Error(`skip_if closure ${c.id} returned ${typeof v}, not a boolean`);
      return v;
    }
    case "cache_key": {
      const v: unknown = await c.fn(ctx);
      if (v !== null && typeof v !== "string") throw new Error(`cache_key closure ${c.id} returned ${typeof v}, not a string or null`);
      return v;
    }
    case "after_run":
      await c.fn(ctx, input);
      return null;
    default:
      await c.fn(ctx);
      return null;
  }
}

function afterRunError(v: unknown): Error | undefined {
  const input = typeof v === "object" && v !== null ? (v as Record<string, unknown>) : {};
  if (input["outcome"] === "success") return undefined;
  if (input["outcome"] !== "failed") throw new ProtocolError("bad_request", "after_run input needs an outcome of success or failed");
  return new Error(typeof input["error"] === "string" ? input["error"] : "");
}

function failureOf(v: unknown): Failure | undefined {
  if (typeof v !== "object" || v === null) return undefined;
  const f = v as Record<string, unknown>;
  return {
    node: String(f["node"] ?? ""),
    stage: f["stage"] === "verify" ? "verify" : "action",
    message: String(f["message"] ?? ""),
  };
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

function str(req: Request, key: string): string {
  const v = req[key];
  if (typeof v !== "string" || v === "") throw new ProtocolError("bad_request", `${key} must be a non-empty string`);
  return v;
}

function stringMap(v: unknown, key: string): Record<string, string> {
  if (typeof v !== "object" || v === null || Array.isArray(v))
    throw new ProtocolError("bad_request", `${key} must be an object of strings`);
  for (const [k, s] of Object.entries(v)) {
    if (typeof s !== "string") throw new ProtocolError("bad_request", `${key}.${k} must be a string`);
  }
  return v as Record<string, string>;
}

// safety: a value with no JSON form never leaves this process, so only what
// can be measured is held to the output limit.
function jsonSize(value: unknown): number {
  try {
    return Buffer.byteLength(JSON.stringify(value) ?? "");
  } catch {
    return 0;
  }
}
