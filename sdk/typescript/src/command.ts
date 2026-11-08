import { isAbsolute, resolve } from "node:path";
import type { Cmd, Context, ExecOptions, ExecResult } from "../index.d.ts";
import { assertExecution, projectRoot } from "./scope.ts";

export const exitNotStarted = -1;
type Termination = "" | "cancellation" | "signal";
type Emitter = (event: string, attrs: Record<string, unknown>) => void;
interface CommandBinding {
  emit: Emitter;
  env: Readonly<Record<string, string>>;
  hostExec?: HostExec;
}
export interface HostExecRequest {
  onOutput?:(stream:string,data:Buffer)=>void;
  step?: string;
  argv: readonly string[];
  dir: string;
  env: string[];
  capture: boolean;
  stdin?: string;
}
// Logged commands return at most the last 64 KiB of each stream; the job log keeps the rest.
export interface HostExecResult {
  exitCode: number;
  signaled: boolean;
  startError?: string;
  notFound: boolean;
  stdout: Buffer;
  stderr: Buffer;
}
export type HostExec = (request: HostExecRequest, signal: AbortSignal) => Promise<HostExecResult>;
const bindings = new WeakMap<Context, CommandBinding>();

export class ExecError extends Error implements ExecResult {
  readonly command: string;
  readonly stdout: string;
  readonly stderr: string;
  readonly exitCode: number;
  readonly terminated: Termination;

  constructor(result: ExecResult, terminated: Termination = "", cause?: unknown) {
    const command = boundedCommand(result.command);
    let message = terminated
      ? `command terminated by ${terminated}: ${command}`
      : result.exitCode === exitNotStarted
        ? `command failed to start: ${command}`
        : `command failed (exit ${result.exitCode}): ${command}`;
    const output = result.stderr.trim() || result.stdout.trim();
    if (cause instanceof Error && (terminated || result.exitCode === exitNotStarted)) message += `: ${cause.message}`;
    else if (output) message += `\n${output}`;
    super(message, cause === undefined ? undefined : { cause });
    this.name = "ExecError";
    this.command = result.command;
    this.stdout = result.stdout;
    this.stderr = result.stderr;
    this.exitCode = result.exitCode;
    this.terminated = terminated;
  }
}

const execErrorCommandBytes = 512;

// The message repeats a bounded prefix so a generated argument list cannot bury the failure; command keeps it whole.
function boundedCommand(command: string): string {
  const bytes = Buffer.from(command);
  if (bytes.length <= execErrorCommandBytes) return command;
  let cut = execErrorCommandBytes;
  while (cut > 0 && ((bytes[cut] ?? 0) & 0xc0) === 0x80) cut--;
  return `${bytes.subarray(0, cut).toString()}... [${bytes.length - cut} more bytes]`;
}

export function registerCommandContext(ctx: Context, emit: Emitter, env: Readonly<Record<string, string>> = {}, hostExec?: HostExec): void {
  bindings.set(ctx, { emit, env: { ...env }, ...(hostExec === undefined ? {} : { hostExec }) });
}

export function commandEnvironment(ctx:Context):Readonly<Record<string,string>>{return binding(ctx).env}

function binding(ctx: Context): CommandBinding {
  return bindings.get(ctx) ?? { emit: (event, attrs) => ctx.log.info(event, attrs), env: {} };
}

function deriveContext(ctx: Context, signal: AbortSignal, env: Readonly<Record<string, string>>): Context {
  const derived: Context = Object.create(ctx);
  const registered = bindings.has(ctx);
  Object.defineProperties(derived, {
    signal: { value: signal, enumerable: true },
    exec: {
      value: (argv: readonly [string, ...string[]], options?: ExecOptions) =>
        registered ? createCommand(derived, argv, options) : ctx.exec.call(derived, argv, { ...options, env: { ...env, ...options?.env } }),
    },
    bash: {
      value: (script: string, options?: ExecOptions) =>
        registered
          ? createCommand(derived, ["bash", "-c", script], options)
          : ctx.bash.call(derived, script, { ...options, env: { ...env, ...options?.env } }),
    },
  });
  if (registered) registerCommandContext(derived, binding(ctx).emit, env, binding(ctx).hostExec);
  return derived;
}

export function withCommandEnv(ctx: Context, env: Readonly<Record<string, string>>): Context {
  return deriveContext(ctx, ctx.signal, { ...binding(ctx).env, ...env });
}

export function withSignal(ctx: Context, signal: AbortSignal): Context {
  return deriveContext(ctx, signal, binding(ctx).env);
}

export function cleanupContext(ctx: Context): Context {
  return withSignal(ctx, new AbortController().signal);
}

export function createCommand(ctx: Context, argv: readonly [string, ...string[]], options: ExecOptions = {}): Cmd {
  let cwd = options.cwd;
  const env: Record<string, string> = Object.assign(Object.create(null), options.env);
  const args: [string, ...string[]] = [argv[0], ...argv.slice(1)];
  const execute = (capture: boolean) => runCommand(ctx, args, { ...(cwd === undefined ? {} : { cwd }), env, capture });
  return {
    dir(value) {
      cwd = value;
      return this;
    },
    env(name, value) {
      env[name] = value;
      return this;
    },
    envMap(values) {
      Object.assign(env, values);
      return this;
    },
    run() {
      return execute(false);
    },
    capture() {
      return execute(true);
    },
    async string() {
      return (await execute(true)).stdout.trim();
    },
    async lines() {
      return (await execute(true)).stdout
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean);
    },
    async json<T>() {
      const result = await execute(true);
      try {
        return JSON.parse(result.stdout) as T;
      } catch (cause) {
        throw new Error(`parse JSON from ${result.command}`, { cause });
      }
    },
    async mustBeEmpty(reason) {
      const output = (await execute(true)).stdout.trim();
      if (output) throw new Error(`${reason}:\n${output}`);
    },
  };
}

export interface CommandOptions extends ExecOptions {
  input?: string;
  stdout?: (bytes: Uint8Array) => void;
  stderr?: (bytes: Uint8Array) => void;
  capture?: boolean;
}

function commandEnv(ctx:Context,options:ExecOptions):NodeJS.ProcessEnv{return {...process.env,...binding(ctx).env,...options.env};}

export function runInput(
  ctx: Context,
  argv: readonly [string, ...string[]],
  input: string,
  options: ExecOptions = {},
): Promise<ExecResult> {
  return runCommand(ctx, argv, { ...options, input, capture: true });
}

export async function runCommand(ctx: Context, argv: readonly [string, ...string[]], options: CommandOptions = {}): Promise<ExecResult> {
  assertExecution();
  const command = argv[0] === "bash" && argv[1] === "-c" && argv.length === 3 ? argv[2]! : argv.join(" ");
  const emit = binding(ctx).emit;
  const started = performance.now();
  let result: ExecResult = { command, stdout: "", stderr: "", exitCode: exitNotStarted };
  let terminated: Termination = "";
  emit("exec_start", { command });
  try {
    if (ctx.signal.aborted) throw new ExecError(result, "cancellation", ctx.signal.reason);
    const cwd = options.cwd !== undefined && isAbsolute(options.cwd) ? options.cwd : resolve(projectRoot(), options.cwd ?? "");
    const hostExec = binding(ctx).hostExec;
    if (!hostExec) throw new Error("host command service unavailable");
    const reply = await hostCommand(ctx, hostExec, argv, cwd, options);
    result = { command, stdout: reply.stdout.toString("utf8"), stderr: reply.stderr.toString("utf8"), exitCode: reply.exitCode };
    if (reply.startError !== undefined) {
      result = { command, stdout: "", stderr: "", exitCode: exitNotStarted };
      throw new ExecError(result, "", Object.assign(new Error(reply.startError), reply.notFound ? { code: "ENOENT" } : {}));
    }
    if (ctx.signal.aborted && (reply.signaled || reply.exitCode !== 0)) {
      result = { ...result, exitCode: exitNotStarted };
      throw new ExecError(result, "cancellation", ctx.signal.reason);
    }
    if (reply.signaled) throw new ExecError(result, "signal", new Error("process received a signal"));
    if (result.exitCode !== 0) throw new ExecError(result);
    return result;
  } catch (cause) {
    const error = cause instanceof ExecError ? cause : new ExecError(result, terminated, cause);
    terminated = error.terminated;
    throw error;
  } finally {
    emit("exec_end", { command, exit_code: result.exitCode, duration_ms: performance.now() - started, terminated });
  }
}

function hostCommand(ctx: Context, hostExec: HostExec, argv: readonly string[], dir: string, options: CommandOptions): Promise<HostExecResult> {
  const env: string[] = [];
  for (const [name, value] of Object.entries(commandEnv(ctx, options))) if (value !== undefined) env.push(`${name}=${value}`);
  return hostExec(
    {
      ...(ctx.stepId === undefined ? {} : { step: ctx.stepId }),
      argv,
      onOutput:(stream,data)=>(stream==="stdout"?options.stdout:options.stderr)?.(data),
      dir,
      env,
      capture: options.capture === true || !!options.stdout || !!options.stderr,
      ...(options.input === undefined ? {} : { stdin: options.input }),
    },
    ctx.signal,
  );
}
