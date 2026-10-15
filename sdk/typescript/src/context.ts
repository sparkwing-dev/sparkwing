import type { Annotation, Args, Context, Failure, PipelineDef, RunContext } from "../index.d.ts";
import { createCommand, registerCommandContext } from "./command.ts";
import { SDKError, failure } from "./errors.ts";
import { LogWriter, type LineSink } from "./log.ts";
import type { HostServices } from "./routes.ts";

export interface ContextOptions {
  run: RunContext;
  jobId: string;
  stepId?: string;
  args: Args;
  failure?: Failure;
  signal: AbortSignal;
  frames: LineSink;
  definition: PipelineDef;
  services?: HostServices;
  outputs: ReadonlyMap<string, unknown>;
  bodyEnv?: Record<string, string>;
}
export function makeContext(options: ContextOptions): Context {
  const { run, signal, jobId } = options;
  const log = new LogWriter(options.frames, options.stepId);
  const services = (): HostServices => {
    signal.throwIfAborted();
    if (!options.services) throw failure("unavailable", "this process has no bound host services");
    return options.services;
  };
  const read = async (name: string, variable: boolean): Promise<string | undefined> => {
    const declaration = options.definition.secrets?.find((s) => s.name === name && Boolean(s.variable) === variable);
    if (!declaration) throw failure("forbidden", "value was not declared by this pipeline", { reason: "secret_undeclared" });
    try {
      return await services().secret(name, signal);
    } catch (error) {
      if (declaration.optional && error instanceof SDKError && error.code === "not_found") return undefined;
      throw error;
    }
  };
  const ctx: Context = {
    run,
    jobId,
    args: options.args,
    dryRun: run.dryRun,
    runner: run.runner,
    signal,
    log,
    ...(options.stepId === undefined ? {} : { stepId: options.stepId }),
    ...(options.failure === undefined ? {} : { failure: options.failure }),
    secret: (name) => read(name, false),
    variable: (name) => read(name, true),
    output: (job) => services().output(typeof job === "string" ? job : job.id, signal),
    stepOutput: (step) => options.outputs.get(typeof step === "string" ? step : step.id) as never,
    annotate: async (annotation) => {
      checkAnnotation(annotation);
      await services().annotate(annotation, signal);
    },
    summary: async (markdown) => {
      checkSize(markdown);
      await services().summary(markdown, signal);
    },
    mask: (value) => services().mask(value, signal),
    oidcToken: (audience) => services().oidcToken(audience, signal),
    runPipeline: (pipeline, job, child = {}) => services().runPipeline(pipeline, job, child, signal),
    latest: (ref) => services().latest(ref, signal),
    cleanup: (spec) => services().cleanup(spec, signal),
    exec: (argv, opts) => createCommand(ctx, argv, opts),
    bash: (script, opts) => createCommand(ctx, ["bash", "-c", script], opts),
  };
  registerCommandContext(ctx, (event, attrs) => log.event(event, attrs), options.bodyEnv);
  return ctx;
}
function checkSize(value: unknown): void {
  if (Buffer.byteLength(typeof value === "string" ? value : JSON.stringify(value)) > 128 << 10)
    throw failure("bad_request", "annotation or summary exceeds 131072 bytes");
}
function checkAnnotation(annotation: Annotation): void {
  checkSize(annotation);
  if (typeof annotation.message !== "string") throw failure("bad_request", "annotation message must be a string");
  if (annotation.level !== undefined && !["debug", "info", "warn", "error"].includes(annotation.level))
    throw failure("bad_request", "invalid annotation level");
  for (const value of [annotation.line, annotation.endLine, annotation.column])
    if (value !== undefined && (!Number.isSafeInteger(value) || value < 0))
      throw failure("bad_request", "annotation location must be a nonnegative integer");
}
