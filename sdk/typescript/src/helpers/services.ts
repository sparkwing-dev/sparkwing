import { randomBytes } from "node:crypto";
import type { Awaitable, CleanupRegistration, Context, services as ServicesAPI } from "../../index.d.ts";
import { assertExecution } from "../scope.ts";
import { cleanupContext, withSignal } from "../command.ts";
import { dockerCommand, removeContainer } from "./docker.ts";

const autoPort = -1;
const defaultReadyTimeoutMs = 30000;
const cleanupProgram = `
const { execFileSync } = require("node:child_process");
const ids = execFileSync("docker", ["ps", "--all", "--quiet", "--no-trunc", "--filter", "label=" + process.argv[1]], { encoding: "utf8" }).trim().split(/\\s+/).filter(Boolean);
if (ids.some(id => !/^[a-f0-9]{64}$/.test(id))) throw new Error("Docker returned an invalid container ID");
if (ids.length) execFileSync("docker", ["rm", "-f", ...ids], { stdio: "inherit" });
`;

function delay(ms: number, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, ms);
    function abort(): void {
      clearTimeout(timer);
      reject(signal.reason);
    }
    signal.addEventListener("abort", abort, { once: true });
  });
}

async function ready(ctx: Context, spec: ServicesAPI.Service, name: string): Promise<void> {
  if (!spec.readyCmd) {
    await delay(2000, ctx.signal);
    return;
  }
  const timeout = spec.readyTimeoutMs || defaultReadyTimeoutMs;
  const deadline = new AbortController();
  const timer = setTimeout(() => deadline.abort(new Error(`Service ${name} did not become ready within ${timeout}ms`)), timeout);
  const signal = AbortSignal.any([ctx.signal, deadline.signal]);
  try {
    for (;;) {
      signal.throwIfAborted();
      const attempt = new AbortController();
      const attemptTimer = setTimeout(() => attempt.abort(new Error(`Service ${name} readiness command timed out`)), 1500);
      try {
        await dockerCommand(withSignal(ctx, AbortSignal.any([signal, attempt.signal])), "exec", name, "sh", "-c", spec.readyCmd);
        signal.throwIfAborted();
        return;
      } catch (error) {
        signal.throwIfAborted();
        ctx.log.debug("Service readiness command failed", { name, error: String(error) });
      } finally {
        clearTimeout(attemptTimer);
      }
      await delay(500, signal);
    }
  } finally {
    clearTimeout(timer);
  }
}

function nameFor(spec: ServicesAPI.Service): string {
  if (spec.name) return spec.name;
  const image = spec.image.split("@")[0]!.split("/").at(-1)!.split(":")[0]!;
  return (image.replace(/[^a-zA-Z0-9_.-]/g, "-") || "service") + "-" + randomBytes(3).toString("hex");
}

function validate(spec: ServicesAPI.Service): void {
  if (!spec.image) throw new Error("Service image is required");
  const port = spec.port ?? 0,
    host = spec.hostPort ?? 0;
  if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error("Service port must be between 0 and 65535");
  if (!Number.isInteger(host) || host < -1 || host > 65535) throw new Error("Service hostPort must be autoPort or between 0 and 65535");
  if (!port && host) throw new Error("Service hostPort requires a container port");
  if (!port && process.platform !== "linux") throw new Error("Service host networking requires Linux");
  if (spec.readyTimeoutMs !== undefined && (!Number.isFinite(spec.readyTimeoutMs) || spec.readyTimeoutMs < 0))
    throw new Error("Service readiness timeout must be nonnegative");
}

async function withServicesAddrs(
  ctx: Context,
  specs: readonly ServicesAPI.Service[],
  fn: (ctx: Context, addrs: readonly ServicesAPI.Addr[]) => Awaitable<void>,
): Promise<void> {
  assertExecution();
  const resolved = specs.map((spec) => {
    validate(spec);
    return { ...spec, name: nameFor(spec) };
  });
  const owned: { name?: string; cleanup: readonly [string, ...string[]]; registration: CleanupRegistration }[] = [];
  const containers: string[] = [];
  const addrs: ServicesAPI.Addr[] = [];
  let failed = false;
  try {
    for (const spec of resolved) {
      const label = "dev.sparkwing.service=" + randomBytes(16).toString("hex");
      const cleanup: [string, ...string[]] = [process.execPath, "-e", cleanupProgram, label];
      const registration = await ctx.cleanup({ argv: cleanup, description: `Service ${spec.name}` });
      const resource: (typeof owned)[number] = { cleanup, registration };
      owned.push(resource);
      const args = ["create", "--name", spec.name, "--label", label];
      const hostPort = spec.hostPort || spec.port || 0;
      if (spec.port) args.push("-p", `127.0.0.1:${hostPort === autoPort ? "" : hostPort}:${spec.port}`);
      else args.push("--network=host");
      for (const key of Object.keys(spec.env ?? {}).sort()) args.push("-e", `${key}=${spec.env![key]}`);
      args.push(spec.image);
      const created = await dockerCommand(ctx, ...args);
      const id = created.stdout.trim();
      if (!/^[a-f0-9]{12,64}$/.test(id)) throw new Error(`Docker did not report a container ID for ${spec.name}`);
      resource.name = id;
      containers.push(id);
      await dockerCommand(ctx, "start", id);
      let actual = hostPort;
      if (hostPort === autoPort) {
        const result = await dockerCommand(ctx, "port", id, `${spec.port}/tcp`);
        const match = /^127\.0\.0\.1:(\d+)\s*$/m.exec(result.stdout);
        actual = Number(match?.[1]);
        if (!Number.isInteger(actual) || actual < 1 || actual > 65535)
          throw new Error(`Docker did not report a host port for ${spec.name}`);
      }
      addrs.push({ name: spec.name, hostPort: actual });
    }
    for (let i = 0; i < resolved.length; i++) await ready(ctx, resolved[i]!, containers[i]!);
    ctx.signal.throwIfAborted();
    await fn(ctx, addrs);
    ctx.signal.throwIfAborted();
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    const errors: unknown[] = [];
    for (const { name, cleanup, registration } of owned.toReversed()) {
      try {
        if (name) await removeContainer(ctx, name, registration);
        else {
          await cleanupContext(ctx).exec(cleanup).run();
          await registration.unregister();
        }
      } catch (error) {
        errors.push(error);
        ctx.log.warn("Service cleanup failed; host cleanup remains registered", { name, error: String(error) });
      }
    }
    if (!failed && errors.length) throw new AggregateError(errors, "Service cleanup failed");
  }
}

export const services: typeof import("../../index.d.ts").services = {
  autoPort,
  defaultReadyTimeoutMs,
  withServicesAddrs,
  withServices: (ctx: Context, specs: readonly ServicesAPI.Service[], fn: (ctx: Context) => Awaitable<void>): Promise<void> =>
    withServicesAddrs(ctx, specs, fn),
};
