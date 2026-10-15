import type { Cmd, Context, ExecOptions, ExecResult } from "../index.d.ts";
import { Git } from "../src/index.ts";

export interface Call {
  argv: readonly string[];
  options: ExecOptions;
  signal: AbortSignal;
}

export function fakeContext(
  handler: (call: Call) => string | Partial<ExecResult> | Promise<string | Partial<ExecResult>>,
  signal = new AbortController().signal,
): { ctx: Context; calls: Call[]; registrations: string[][]; unregistered: string[]; messages: unknown[] } {
  const calls: Call[] = [],
    registrations: string[][] = [],
    unregistered: string[] = [],
    messages: unknown[] = [];
  const logger = (message: string, attrs?: Record<string, unknown>): void => {
    messages.push({ message, attrs });
  };
  const ctx: Context = {
    run: {
      id: "run",
      pipeline: "check",
      args: {},
      dryRun: false,
      noMemo: false,
      runner: { name: "runner", type: "test", labels: [] },
      git: new Git("", { repo: "owner/repo", branch: "main", commit: "abc" }),
      trigger: { source: "test" },
    },
    jobId: "job",
    args: {},
    dryRun: false,
    runner: { name: "runner", type: "test", labels: [] },
    signal,
    log: { debug: logger, info: logger, warn: logger, error: logger },
    async secret() {
      return undefined;
    },
    async variable() {
      return undefined;
    },
    async output() {
      return undefined;
    },
    stepOutput() {
      return undefined;
    },
    async annotate() {},
    async summary() {},
    async mask(value) {
      messages.push({ mask: value });
    },
    async oidcToken() {
      return "";
    },
    async runPipeline() {
      throw new Error("unexpected child");
    },
    async latest() {
      throw new Error("unexpected reference");
    },
    async cleanup(spec) {
      registrations.push([...spec.argv]);
      const id = `cleanup-${registrations.length}`;
      return {
        id,
        async unregister() {
          unregistered.push(id);
        },
      };
    },
    exec(argv, supplied = {}) {
      const bound = this;
      let options = { ...supplied };
      async function execute(): Promise<ExecResult> {
        bound.signal.throwIfAborted();
        const call = { argv, options, signal: bound.signal };
        calls.push(call);
        const value = await handler(call);
        return { command: argv.join(" "), stdout: "", stderr: "", exitCode: 0, ...(typeof value === "string" ? { stdout: value } : value) };
      }
      const cmd: Cmd = {
        dir(cwd) {
          options = { ...options, cwd };
          return this;
        },
        env(name, value) {
          options = { ...options, env: { ...options.env, [name]: value } };
          return this;
        },
        envMap(env) {
          options = { ...options, env: { ...options.env, ...env } };
          return this;
        },
        run: execute,
        capture: execute,
        async string() {
          return (await execute()).stdout.trim();
        },
        async lines() {
          return (await execute()).stdout
            .split("\n")
            .map((line) => line.trim())
            .filter(Boolean);
        },
        async json<T>() {
          return JSON.parse((await execute()).stdout) as T;
        },
        async mustBeEmpty(reason) {
          if ((await execute()).stdout.trim()) throw new Error(reason);
        },
      };
      return cmd;
    },
    bash() {
      throw new Error("unexpected bash");
    },
  };
  return { ctx, calls, registrations, unregistered, messages };
}

export function commandFailure(message: string, exitCode = 1): Error & { exitCode: number } {
  return Object.assign(new Error(message), { exitCode });
}
