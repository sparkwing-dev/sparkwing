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
import { StepState } from "./step-state.ts";

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
  done: Promise<void>;
  finish: () => void;
}

export async function serve(options: ServeOptions): Promise<void> {
  const session = new Session(options);
  try {
    for await (const request of readRequests(options.input)) session.answer(request);
  } finally {
    await session.close();
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
  readonly #steps = new Map<string, StepState>();
  readonly #generated = new Map<string, unknown>();
  #evalReady: Promise<void> = Promise.resolve();
  readonly #originalEnv = new Map<string, string | undefined>();
  constructor(options: ServeOptions) {
    this.#options = options;
    this.#frames = {
      write: (line) => {
        if (!this.#done) options.output.write(line);
      },
    };
  }
  async close(): Promise<void> {
    this.#done = true;
    for (const { controller } of this.#active.values()) controller.abort(new Error("host closed request pipe"));
    await Promise.all([...this.#active.values()].map((active) => active.done));
    for (const [key, value] of this.#originalEnv) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
  answer(req: Request): void {
    if (this.#ids.has(req.id)) throw failure("protocol_error", "duplicate request id");
    this.#ids.add(req.id);
    void this.#handle(req)
      .then(
        (result) => writeReply(this.#frames, { reply: req.id, ok: true, result }),
        (error) => writeReply(this.#frames, { reply: req.id, ok: false, error: errorFields(error) }),
      )
      .finally(() => {
        const active = this.#active.get(req.id);
        this.#active.delete(req.id);
        active?.finish();
      });
  }
  async #handle(req: Request): Promise<unknown> {
    validateRequest(req);
    if (req.op === "hello") return this.#hello(req);
    if (!this.#runner) throw failure("protocol_error", "hello must precede operations");
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
        return this.#cancel(req);
    }
    throw failure("unknown_op", "unknown operation");
  }
  async #cancel(req: Request): Promise<unknown> {
    if (req.job !== undefined && req.job !== this.#binding?.job)
      throw failure("protocol_error", "cancel does not match the process job binding");
    const selected = [...this.#active.entries()].filter(
      ([id, active]) =>
        req.target === id || (req.target === undefined && active.job === req.job && (req.step === undefined || active.step === req.step)),
    );
    for (const [, active] of selected) active.controller.abort(new Error("cancelled by host"));
    await Promise.all(selected.map(([, active]) => active.done));
    return {};
  }
  #state(job: string): StepState {
    let state = this.#steps.get(job);
    if (!state) {
      state = new StepState();
      this.#steps.set(job, state);
    }
    return state;
  }
  #hello(req: Request): unknown {
    if (this.#helloSeen) throw failure("protocol_error", "hello occurs exactly once");
    this.#helloSeen = true;
    if (req.protocol !== 2) throw failure("protocol_mismatch", "rebuild .sparkwing with the matching SDK", { supported_majors: [2] });
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
    if (this.#planSeen) throw failure("protocol_error", "plan occurs at most once per process");
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
    if (!this.#binding) throw failure("protocol_error", "execution requires a hello binding");
    if (!replay && this.#binding.job !== job) throw failure("protocol_error", "job does not match hello binding");
  }
  #context(req: Request, spec: JobSpec, signal: AbortSignal, step?: string, replay = false): Context {
    const { run, definition, plan, rawArgs } = this.#current();
    for (const [key, value] of this.#originalEnv) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    const env: Record<string, string> = Object.create(null);
    for (const [name, value] of Object.entries(replay ? {} : (spec.env ?? {})))
      if (!value.startup && !["NODE_OPTIONS", "GODEBUG", "GOMAXPROCS"].includes(name)) {
        if (!this.#originalEnv.has(name)) this.#originalEnv.set(name, Object.hasOwn(process.env, name) ? process.env[name] : undefined);
        process.env[name] = value.value;
        env[name] = value.value;
      }
    let failureInfo: Failure | undefined;
    if (req.failure !== undefined) {
      const f = object(req.failure, "failure");
      failureInfo = { job: String(f.job), stage: String(f.stage), message: decodeError(f.error).message };
    }
    const inputs = { ...run.args, ...rawArgs };
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
    const outputs = this.#state(spec.id).outputs;
    return makeContext({
      run,
      jobId: spec.id,
      ...(step === undefined ? {} : { stepId: step }),
      args,
      signal,
      frames: this.#frames,
      definition,
      outputs,
      bodyEnv: env,
      ...(failureInfo === undefined ? {} : { failure: failureInfo }),
      ...(this.#services === undefined ? {} : { services: this.#services }),
    });
  }
  #activate(id: string, step?: string): AbortSignal {
    const controller = new AbortController();
    let finish!: () => void;
    const done = new Promise<void>((resolve) => {
      finish = resolve;
    });
    this.#active.set(id, { job: this.#binding!.job, ...(step === undefined ? {} : { step }), controller, done, finish });
    return controller.signal;
  }
  async #step(req: Request): Promise<unknown> {
    const { plan, run } = this.#current();
    const job = plan.findJob(String(req.job));
    if (!job) throw failure("unknown_job", "job is not in this process's plan");
    const step = job.findStep(String(req.step));
    if (!step) throw failure("unknown_step", "step is not in this job");
    const spec = plan.toDoc().jobs.find((value) => value.id === job.id)!;
    this.#authorize(job.id, false);
    const state = this.#state(job.id);
    state.begin(spec, step.id, Number(req.attempt), req.settled as string[]);
    try {
      const ctx = this.#context(req, spec, this.#activate(req.id, step.id), step.id);
      if (run.dryRun && !step.dryRunBody && !step.toSpec().safe_without_dry_run) throw failure("sdk_error", "step has no dry-run body");
      const body = run.dryRun && step.dryRunBody ? step.dryRunBody : step.body;
      ctx.signal.throwIfAborted();
      const output = jsonOutput(await runScope({ phase: "execute", root: this.#root() }, () => body(ctx)));
      ctx.signal.throwIfAborted();
      state.outputs.set(step.id, output);
      return { outcome: "success", ...(output === undefined ? {} : { output }) };
    } catch (error) {
      return { outcome: "failed", error: errorFields(error) };
    } finally {
      state.finish(step.id, true);
    }
  }
  async #eval(req: Request): Promise<unknown> {
    const { plan } = this.#current();
    const closure = plan.findClosure(String(req.closure));
    if (!closure) throw failure("unknown_closure", "closure is not in this process's plan");
    const spec = plan.toDoc().jobs.find((value) => value.id === closure.job.id)!;
    this.#authorize(spec.id, closure.kind === "generate");
    const signal = this.#activate(req.id, closure.step?.id);
    const previous = this.#evalReady;
    let release!: () => void;
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    this.#evalReady = previous.then(() => released);
    let stepStarted = false;
    try {
      await evalTurn(previous, signal);
      if (closure.step) {
        this.#state(spec.id).begin(spec, closure.step.id, Number(req.attempt), req.settled as string[]);
        stepStarted = true;
      } else if (req.attempt !== undefined || req.settled !== undefined)
        throw failure("protocol_error", "job closure cannot carry step state");
      const ctx = this.#context(req, spec, signal, closure.step?.id, closure.kind === "generate");
      try {
        return { value: jsonOutput(await runScope({ phase: "execute", root: this.#root() }, () => this.#call(closure, ctx, req))) ?? null };
      } catch (error) {
        return { value: null, error: errorFields(error) };
      }
    } finally {
      if (stepStarted) this.#state(spec.id).finish(closure.step!.id, false);
      release();
    }
  }
  async #call(closure: Closure, ctx: Context, req: Request): Promise<unknown> {
    ctx.signal.throwIfAborted();
    if (closure.kind === "generate") {
      if (this.#generated.has(closure.id)) return structuredClone(this.#generated.get(closure.id));
      const generated = new Generated(closure.generator.source, closure.id, this.#current().plan);
      await closure.fn(generated, req.input ?? null, ctx);
      ctx.signal.throwIfAborted();
      const jobs = jsonOutput(generated.toDoc().jobs);
      const { definition, plan, rawArgs } = this.#current();
      resolvedArgs(
        [...(definition.args ?? []), ...[...plan.jobs(), ...generated.jobs()].flatMap((job) => job.argSpecs())],
        rawArgs,
        definition.extra === true,
      );
      plan.insert(generated);
      this.#generated.set(closure.id, jobs);
      return jobs;
    }

    if (closure.kind === "after_run") {
      const result = object(req.input, "after_run input");
      if (typeof result.outcome !== "string" || !["success", "failed"].includes(result.outcome))
        throw failure("bad_request", "after_run requires a valid outcome");
      if ((result.outcome === "failed") !== (result.error !== undefined))
        throw failure("protocol_error", "after_run outcome requires a matching error");
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

async function evalTurn(previous: Promise<void>, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  let abort!: () => void;
  try {
    await Promise.race([
      previous,
      new Promise<never>((_, reject) => {
        abort = () => reject(signal.reason);
        signal.addEventListener("abort", abort, { once: true });
      }),
    ]);
    signal.throwIfAborted();
  } finally {
    signal.removeEventListener("abort", abort);
  }
}

function resolvedArgs(specs: readonly import("../index.d.ts").ArgSpec[], raw: Args, allowUnknown: boolean): Record<string, string> {
  try {
    return resolveArgs(specs, raw, { allowUnknown });
  } catch (error) {
    throw failure("invalid_args", messageOf(error));
  }
}
