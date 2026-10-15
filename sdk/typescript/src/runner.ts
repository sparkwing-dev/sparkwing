import { readFileSync } from "node:fs";
import type { Readable } from "node:stream";
import type { Args, Context, Failure, JobResult, PipelineDef, RunContext, RunnerInfo, Trigger } from "../index.d.ts";
import { describe } from "./describe.ts";
import { resolveArgs } from "./args.ts";
import { Generated, lookupPipeline, Plan, type Closure } from "./plan.ts";
import type { JobSpec } from "./plan-wire.ts";
import { Git } from "./helpers/git.ts";
import { noMemo } from "./helpers/memo.ts";
import { makeContext } from "./context.ts";
import { SDKError, decodeError, errorFields, failure, messageOf, object, string } from "./errors.ts";
import type { LineSink } from "./log.ts";
import { jsonOutput, readRequests, stringMap, validateRequest, writeReply, type Request } from "./protocol.ts";
import { NodeRoutes, type HostServices, type ProcessRoutes } from "./routes.ts";
import { runScope } from "./scope.ts";

export interface ServeOptions {
  input: Readable;
  output: LineSink;
  services?: (routes: ProcessRoutes) => HostServices;
  root?: string;
}
interface Planned {
  plan: Plan;
  run: RunContext;
  definition: PipelineDef;
  rawArgs: Args;
}
interface Active {
  job: string;
  step?: string;
  controller: AbortController;
}

export async function serve(options: ServeOptions): Promise<void> {
  const session = new Session(options);
  try {
    for await (const request of readRequests(options.input)) session.answer(request);
  } finally {
    session.close();
  }
}
class Session {
  readonly #options: ServeOptions;
  readonly #frames: LineSink;
  #done = false;
  #helloSeen = false;
  #runner: RunnerInfo | undefined;
  #binding: { run: string; job: string } | undefined;
  #services: HostServices | undefined;
  #planSeen = false;
  #planned: Planned | undefined;
  readonly #ids = new Set<string>();
  readonly #active = new Map<string, Active>();
  readonly #outputs = new Map<string, Map<string, unknown>>();
  readonly #attempts = new Map<string, number>();
  readonly #generated = new Set<string>();
  readonly #generating = new Set<string>();
  readonly #originalEnv = new Map<string, string | undefined>();
  constructor(options: ServeOptions) {
    this.#options = options;
    this.#frames = {
      write: (line) => {
        if (!this.#done) options.output.write(line);
      },
    };
  }
  close(): void {
    this.#done = true;
    for (const { controller } of this.#active.values()) controller.abort(new Error("host closed request pipe"));
    for (const [key, value] of this.#originalEnv) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  answer(req: Request): void {
    if (this.#ids.has(req.id)) throw failure("protocol_error", "duplicate request id");
    this.#ids.add(req.id);
    void this.#handle(req).then(
      (result) => writeReply(this.#frames, { reply: req.id, ok: true, result }),
      (error) => writeReply(this.#frames, { reply: req.id, ok: false, error: errorFields(error) }),
    );
  }
  async #handle(req: Request): Promise<unknown> {
    validateRequest(req);
    if (req.op === "hello") return this.#hello(req);
    if (!this.#runner) throw failure("bad_request", "hello must precede operations");
    switch (req.op) {
      case "describe":
        return runScope({ phase: "plan", root: this.#root() }, () => describe(this.#run({ run_id: "describe", pipeline: "describe" }, {})));
      case "plan":
        return this.#plan(req);
      case "run_step":
        return this.#step(req);
      case "eval":
        return this.#eval(req);
      case "cancel":
        for (const active of this.#active.values())
          if (active.job === req.job && (req.step === undefined || active.step === req.step))
            active.controller.abort(new Error("cancelled by host"));
        return {};
    }
    throw failure("unknown_op", "unknown operation");
  }
  #hello(req: Request): unknown {
    if (this.#helloSeen) throw failure("bad_request", "hello occurs exactly once");
    this.#helloSeen = true;
    if (req.protocol !== 2) throw failure("protocol_mismatch", "rebuild .sparkwing with the matching SDK", { supported: [2] });
    const runner = Object.freeze({ ...(req.runner as RunnerInfo), labels: Object.freeze([...(req.runner as RunnerInfo).labels]) });
    if (req.binding !== undefined) {
      const binding = object(req.binding, "binding");
      this.#binding = { run: String(binding.run_id), job: String(binding.job_id) };
      const routes = binding.routes as unknown as ProcessRoutes;
      this.#services = this.#options.services ? this.#options.services(routes) : new NodeRoutes(routes);
    }
    this.#runner = runner;
    const { version } = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version: string };
    return { protocol: 2, sdk: { language: "typescript", version: `v${version}` }, features: [] };
  }
  #root(): string {
    return this.#options.root ?? process.cwd();
  }
  #run(raw: Record<string, unknown>, args: Args): RunContext {
    const git = raw.git === undefined ? {} : object(raw.git, "run.git");
    const trigger = raw.trigger === undefined ? { source: "manual" } : object(raw.trigger, "run.trigger");
    const pr = trigger.pull_request === undefined ? undefined : object(trigger.pull_request, "pull_request");
    const triggerInfo: Trigger = {
      source: String(trigger.source),
      ...(trigger.user === undefined ? {} : { user: String(trigger.user) }),
      ...(pr
        ? {
            pullRequest: Object.freeze({
              number: Number(pr.number),
              ...(pr.action === undefined ? {} : { action: String(pr.action) }),
              ...(pr.base_ref === undefined ? {} : { baseRef: String(pr.base_ref) }),
              ...(pr.base_sha === undefined ? {} : { baseSHA: String(pr.base_sha) }),
              ...(pr.head_ref === undefined ? {} : { headRef: String(pr.head_ref) }),
              ...(pr.head_sha === undefined ? {} : { headSHA: String(pr.head_sha) }),
            }),
          }
        : {}),
    };
    return Object.freeze({
      id: String(raw.run_id),
      pipeline: String(raw.pipeline),
      args: Object.freeze({ ...args }),
      dryRun: raw.dry_run === true,
      noMemo: raw.no_memo === true,
      ...(raw.started_at === undefined ? {} : { startedAt: String(raw.started_at) }),
      runner: this.#runner!,
      trigger: Object.freeze(triggerInfo),
      git: new Git(this.#root(), {
        repo: String(git.repo ?? ""),
        branch: String(git.branch ?? ""),
        commit: String(git.sha ?? ""),
        ...(git.default_branch === undefined ? {} : { defaultBranch: String(git.default_branch) }),
        ...(git.repo_url === undefined ? {} : { repoURL: String(git.repo_url) }),
      }),
    });
  }
  async #plan(req: Request): Promise<unknown> {
    if (this.#planSeen) throw failure("bad_request", "plan occurs at most once per process");
    this.#planSeen = true;
    const definition = lookupPipeline(String(req.pipeline));
    if (!definition) throw failure("unknown_pipeline", "pipeline is not registered");
    const rawArgs = stringMap(req.args, "args");
    const args = resolvedArgs(definition.args ?? [], rawArgs, true);
    const run = this.#run(object(req.run, "run"), args);
    if (this.#binding && run.id !== this.#binding.run) throw failure("bad_request", "plan run does not match hello binding");
    const plan = new Plan();
    try {
      await runScope({ phase: "plan", root: this.#root() }, () => definition.plan(plan, run));
      const doc = plan.toDoc();
      const allSpecs = [...(definition.args ?? []), ...plan.jobs().flatMap((job) => job.argSpecs())];
      resolvedArgs(allSpecs, rawArgs, definition.extra === true);
      plan.seal();
      this.#planned = { plan, run, definition, rawArgs };
      return doc;
    } catch (error) {
      if (error instanceof SDKError) throw error;
      throw failure("plan_failed", messageOf(error));
    }
  }
  #current(): Planned {
    if (!this.#planned) throw failure("no_plan", "operation requires a successful plan");
    return this.#planned;
  }
  #authorize(job: string, replay: boolean): void {
    if (!this.#binding) throw failure("bad_request", "execution requires a hello binding");
    if (!replay && this.#binding.job !== job) throw failure("forbidden", "job does not match hello binding");
  }
  #context(req: Request, spec: JobSpec, step?: string, replay = false): Context {
    const { run, definition, plan, rawArgs } = this.#current();
    this.#authorize(spec.id, replay);
    for (const [key, value] of this.#originalEnv) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    const env: Record<string, string> = {};
    for (const [name, value] of Object.entries(spec.env ?? {}))
      if (!value.startup && !["NODE_OPTIONS", "GODEBUG", "GOMAXPROCS"].includes(name)) {
        if (!this.#originalEnv.has(name)) this.#originalEnv.set(name, process.env[name]);
        process.env[name] = value.value;
        env[name] = value.value;
      }
    let failureInfo: Failure | undefined;
    if (req.failure !== undefined) {
      const f = object(req.failure, "failure");
      failureInfo = { job: String(f.job), stage: String(f.stage), message: decodeError(f.error).message };
    }
    const inputs = { ...run.args, ...rawArgs, ...(req.args === undefined ? {} : stringMap(req.args, "args")) };
    const pipelineArgs = resolvedArgs(
      [...(definition.args ?? []), ...plan.jobs().flatMap((job) => job.argSpecs())],
      inputs,
      definition.extra === true,
    );
    let args: Args;
    try {
      args = plan.findJob(spec.id)!.resolveArgs(pipelineArgs);
    } catch (error) {
      throw failure("invalid_args", messageOf(error));
    }
    if (req.attempt !== undefined) {
      const attempt = Number(req.attempt);
      const prior = this.#attempts.get(spec.id);
      if (prior !== undefined && attempt < prior) throw failure("bad_request", "attempt ordinal moved backwards");
      if (prior !== attempt) {
        if ([...this.#active.values()].some((active) => active.job === spec.id))
          throw failure("bad_request", "previous attempt is still running");
        this.#attempts.set(spec.id, attempt);
        this.#outputs.delete(spec.id);
      }
    }
    const controller = new AbortController();
    this.#active.set(req.id, { job: spec.id, ...(step === undefined ? {} : { step }), controller });
    const outputs = this.#outputs.get(spec.id) ?? new Map<string, unknown>();
    this.#outputs.set(spec.id, outputs);
    return makeContext({
      run,
      jobId: spec.id,
      ...(step === undefined ? {} : { stepId: step }),
      args,
      signal: controller.signal,
      frames: this.#frames,
      definition,
      outputs,
      bodyEnv: env,
      ...(failureInfo === undefined ? {} : { failure: failureInfo }),
      ...(this.#services === undefined ? {} : { services: this.#services }),
    });
  }
  async #step(req: Request): Promise<unknown> {
    const { plan, run } = this.#current();
    const job = plan.findJob(String(req.job));
    if (!job) throw failure("unknown_job", "job is not in this process's plan");
    const step = job.findStep(String(req.step));
    if (!step) throw failure("unknown_step", "step is not in this job");
    const spec = plan.toDoc().jobs.find((value) => value.id === job.id)!;
    if ([...this.#active.values()].some((active) => active.job === job.id && active.step === step.id))
      throw failure("bad_request", "step is already running");
    const ctx = this.#context(req, spec, step.id);
    try {
      const body = run.dryRun && step.dryRunBody ? step.dryRunBody : step.body;
      ctx.signal.throwIfAborted();
      const output = jsonOutput(await runScope({ phase: "execute", root: this.#root() }, () => body(ctx)));
      ctx.signal.throwIfAborted();
      this.#outputs.get(job.id)!.set(step.id, output);
      return { outcome: "success", ...(output === undefined ? {} : { output }) };
    } catch (error) {
      return { outcome: "failed", error: errorFields(error) };
    } finally {
      this.#active.delete(req.id);
    }
  }
  async #eval(req: Request): Promise<unknown> {
    const { plan } = this.#current();
    const closure = plan.findClosure(String(req.closure));
    if (!closure) throw failure("unknown_closure", "closure is not in this process's plan");
    const spec = plan.toDoc().jobs.find((value) => value.id === closure.job.id)!;
    if ((req.job !== undefined && req.job !== spec.id) || (req.step !== undefined && req.step !== closure.step?.id))
      throw failure("bad_request", "closure attribution does not match declaration");
    const ctx = this.#context(req, spec, closure.step?.id, closure.kind === "generate");
    try {
      return { value: jsonOutput(await runScope({ phase: "execute", root: this.#root() }, () => this.#call(closure, ctx, req))) ?? null };
    } catch (error) {
      return { value: null, error: errorFields(error) };
    } finally {
      this.#active.delete(req.id);
    }
  }
  async #call(closure: Closure, ctx: Context, req: Request): Promise<unknown> {
    ctx.signal.throwIfAborted();
    if (closure.kind === "generate") {
      if (!("input" in req)) throw failure("bad_request", "generation requires recorded source output in input");
      if (this.#generated.has(closure.id) || this.#generating.has(closure.id))
        throw failure("bad_request", "generator already evaluated in this process");
      this.#generating.add(closure.id);
      try {
        const generated = new Generated(closure.generator.source, closure.id, this.#current().plan);
        await closure.fn(generated, req.input, ctx);
        ctx.signal.throwIfAborted();
        const jobs = jsonOutput(generated.toDoc().jobs);
        this.#current().plan.insert(generated);
        this.#generated.add(closure.id);
        return jobs;
      } finally {
        this.#generating.delete(closure.id);
      }
    }

    if (closure.kind === "after_run") {
      const result = object(req.input, "after_run input");
      if (
        typeof result.outcome !== "string" ||
        !["success", "failed", "cancelled", "skipped", "superseded", "memoized"].includes(result.outcome)
      )
        throw failure("bad_request", "after_run requires a valid outcome");
      const finalResult = { ...result } as unknown as JobResult<unknown>;
      if (result.error !== undefined) {
        const error = decodeError(result.error);
        Object.assign(finalResult, {
          failure: { job: ctx.jobId, stage: result.stage === "verify" ? "verify" : "action", message: error.message },
        });
      }
      await closure.fn(ctx, finalResult);
      ctx.signal.throwIfAborted();
      return null;
    }
    const result = await closure.fn(ctx);
    ctx.signal.throwIfAborted();
    if (closure.kind === "skip_if") {
      if (typeof result !== "boolean") throw failure("sdk_error", "skip predicate must return a boolean");
      return result;
    }
    if (closure.kind === "memo_key") {
      if (result === noMemo) return null;
      if (typeof result !== "string" || result === "") throw failure("sdk_error", "memo key must be nonempty or noMemo");
      return result;
    }
    return null;
  }
}

function resolvedArgs(specs: readonly import("../index.d.ts").ArgSpec[], raw: Args, allowUnknown: boolean): Record<string, string> {
  try {
    return resolveArgs(specs, raw, { allowUnknown });
  } catch (error) {
    throw failure("invalid_args", messageOf(error));
  }
}
