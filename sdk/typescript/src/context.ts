import type { Annotation, Args, Context, Failure, PipelineDef, RunContext } from "../index.d.ts";
import { createCommand, registerCommandContext, type HostExec } from "./command.ts";
import { SDKError, failure } from "./errors.ts";
import { LogWriter, type LineSink } from "./log.ts";
import { registerHelpers } from "./helpers/host.ts";
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
  hostExec?: HostExec;
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
    try {
      return await services().secret(name, signal, variable);
    } catch (error) {
      if (declaration?.optional && error instanceof SDKError && error.code === "not_found") return undefined;
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
      await services().annotate(annotation, signal, options.stepId);
    },
    summary: async (markdown) => {
      await services().summary(markdown, signal, options.stepId);
    },
    mask: (value) => services().mask(value, signal),
    oidcToken: (audience) => services().oidcToken(audience, signal),
    runPipeline: (pipeline, job, child = {}) => services().runPipeline(pipeline, job, child, signal),
    latest: (ref) => services().latest(ref, signal),
    cleanup: (spec) => services().cleanup(spec, signal),
    exec: (argv, opts) => createCommand(ctx, argv, opts),
    bash: (script, opts) => createCommand(ctx, ["bash", "-c", script], opts),
  };
  registerHelpers(ctx, options.services);
  registerCommandContext(ctx, (event, attrs) => log.event(event, attrs), options.bodyEnv, options.hostExec);
  return ctx;
}
