import { randomBytes } from "node:crypto";
import * as fs from "node:fs/promises";
import * as paths from "node:path";
import { tmpdir } from "node:os";
import type { CleanupRegistration, Context, docker as DockerAPI, ExecResult } from "../../index.d.ts";
import { assertExecution } from "../scope.ts";
import { cleanupContext, runCommand, runInput } from "../command.ts";
import { isMissing, path } from "./files.ts";
import { git } from "./git.ts";

export class DockerUnavailableError extends Error {}
export class BuildxRequiredError extends Error {}
export class PlatformUnsupportedError extends Error {}

function unavailable(error: unknown): boolean {
  if (isMissing(error)) return true;
  return error instanceof Error && error.cause !== undefined && unavailable(error.cause);
}

export async function dockerCommand(ctx: Context, ...args: string[]): Promise<ExecResult> {
  assertExecution();
  try {
    return await ctx.exec(["docker", ...args]).capture();
  } catch (cause) {
    if (unavailable(cause)) throw new DockerUnavailableError("Docker binary is not available on PATH", { cause });
    throw cause;
  }
}

async function buildxPlatforms(ctx: Context): Promise<string[]> {
  try {
    await dockerCommand(ctx, "buildx", "version");
  } catch (cause) {
    ctx.signal.throwIfAborted();
    if (cause instanceof DockerUnavailableError) throw cause;
    throw new BuildxRequiredError("Docker buildx is required", { cause });
  }
  const result = await dockerCommand(ctx, "buildx", "inspect");
  const found = result.stdout
    .split("\n")
    .filter((line) => line.trim().startsWith("Platforms:"))
    .flatMap((line) => line.slice(line.indexOf(":") + 1).split(","))
    .map((value) => value.trim().replace(/\*$/, ""))
    .filter(Boolean);
  return [...new Set(found)];
}

function advertised(wish: string, targets: readonly string[]): boolean {
  return targets.some((target) => target === wish || target.startsWith(wish + "/"));
}

function entries(values: Readonly<Record<string, string>> | undefined): [string, string][] {
  return Object.entries(values ?? {}).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
}

function digest(output: string, tag: string): string | undefined {
  const found = output
    .split("\n")
    .filter((line) => line.startsWith(`${tag}: digest: `))
    .map((line) => /^.+: digest: (sha256:[a-f0-9]{64}) size: \d+\s*$/.exec(line)?.[1])
    .filter(Boolean);
  const unique = new Set(found);
  return unique.size === 1 ? found[0] : undefined;
}

async function pushRefs(
  ctx: Context,
  local: string,
  image: string,
  tags: readonly string[],
  registries: readonly string[],
): Promise<DockerAPI.BuildResult> {
  const digests: Record<string, string> = {};
  const completed: string[] = [],
    errors: unknown[] = [];
  let first = local;
  let pushed = false;
  for (const registry of registries) {
    let success = true;
    for (const tag of tags) {
      ctx.signal.throwIfAborted();
      const remote = `${registry}/${image}:${tag}`;
      try {
        await dockerCommand(ctx, "tag", local, remote);
        const output = await dockerCommand(ctx, "push", remote);
        const value = digest(output.stdout + "\n" + output.stderr, tag);
        if (value) digests[remote] = value;
        if (!pushed) {
          first = remote;
          pushed = true;
        }
      } catch (error) {
        success = false;
        errors.push(error);
      }
    }
    if (success) completed.push(registry);
  }
  if (errors.length) throw new AggregateError(errors, "Docker image push failed");
  return { image: first, digests, registries: completed };
}

async function build(ctx: Context, config: DockerAPI.BuildConfig, push: boolean): Promise<DockerAPI.BuildResult> {
  assertExecution();
  if (!config.image || !config.tags.length || config.tags.some((tag) => !tag))
    throw new Error("Docker build requires an image and nonempty tags");
  const platforms = config.platforms ?? [],
    registries = config.registries ?? [];
  const multi = platforms.length > 0;
  if (multi) {
    const targets = await buildxPlatforms(ctx);
    const missing = platforms.filter((target) => !advertised(target, targets));
    if (targets.length && missing.length)
      throw new PlatformUnsupportedError(`Unsupported platforms: ${missing.join(", ")}; builder advertises ${targets.join(", ")}`);
  }
  const local = config.tags.map((tag) => `${config.image}:${tag}`);
  const remote = registries.flatMap((registry) => config.tags.map((tag) => `${registry}/${config.image}:${tag}`));
  const args = multi ? ["buildx", "build", "--platform", platforms.join(",")] : ["build"];
  if (multi && push && remote.length) args.push("--push");
  else if (platforms.length === 1) args.push("--load");
  args.push("-f", config.dockerfile || "Dockerfile");
  if (!(multi && push && remote.length)) for (const ref of local) args.push("-t", ref);
  if (multi) for (const ref of remote) args.push("-t", ref);
  for (const [key, value] of entries(config.buildArgs)) args.push("--build-arg", `${key}=${value}`);
  for (const [key, value] of entries(config.labels)) args.push("--label", `${key}=${value}`);
  let metadata: string | undefined;
  try {
    if (multi && push && remote.length) {
      metadata = await fs.mkdtemp(paths.join(tmpdir(), "sparkwing-push-"));
      args.push("--metadata-file", paths.join(metadata, "metadata.json"));
    }
    args.push(config.context || ".");
    await dockerCommand(ctx, ...args);
    if (!push || !registries.length) return { image: local[0]!, digests: {}, registries: [] };
    if (!multi) return await pushRefs(ctx, local[0]!, config.image, config.tags, registries);
    const data: unknown = JSON.parse(await fs.readFile(paths.join(metadata!, "metadata.json"), "utf8"));
    const value = typeof data === "object" && data !== null && "containerimage.digest" in data ? data["containerimage.digest"] : undefined;
    const digests: Record<string, string> = {};
    if (typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value)) for (const ref of remote) digests[ref] = value;
    return { image: remote[0]!, digests, registries: [...registries] };
  } finally {
    if (metadata) await fs.rm(metadata, { recursive: true, force: true });
  }
}

export async function removeContainer(ctx: Context, name: string, registration: CleanupRegistration): Promise<void> {
  await dockerCommand(cleanupContext(ctx), "rm", "-f", name);
  await registration.unregister();
}

async function run(ctx: Context, options: DockerAPI.RunOptions): Promise<void> {
  assertExecution();
  if (!options.image) throw new Error("Docker run requires an image");
  if (options.outputDir && !options.outputTo) throw new Error("Docker run outputTo is required for outputDir");
  const name = "sparkwing-run-" + randomBytes(6).toString("hex");
  const working = options.workDir || "/work";
  const args = ["create", "--name", name, "-w", working];
  if (options.user) args.push("--user", options.user);
  for (const [key, value] of entries(options.env)) args.push("-e", `${key}=${value}`);
  for (const [volume, mount] of entries(options.volumes)) {
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(volume)) throw new Error(`Docker volumes require a named volume: ${volume}`);
    args.push("-v", `${volume}:${mount}`);
  }
  args.push(options.image, ...(options.cmd ?? []));
  await dockerCommand(ctx, "pull", options.image);
  const registration = await ctx.cleanup({ argv: ["docker", "rm", "-f", name], description: `Docker run ${options.image}` });
  let failed = false;
  try {
    await dockerCommand(ctx, ...args);
    if (options.inputDir) await dockerCommand(ctx, "cp", path(options.inputDir) + "/.", `${name}:${working}`);
    if (options.stdout || options.stderr) {
      await runCommand(ctx, ["docker", "start", "-a", name], {
        ...(options.stdout ? { stdout: options.stdout } : {}),
        ...(options.stderr ? { stderr: options.stderr } : {}),
      });
    } else await ctx.exec(["docker", "start", "-a", name]).run();
    if (options.outputDir) {
      const destination = path(options.outputTo!);
      await fs.mkdir(destination, { recursive: true });
      await dockerCommand(ctx, "cp", `${name}:${options.outputDir}/.`, destination);
    }
  } catch (error) {
    failed = true;
    throw error;
  } finally {
    try {
      await removeContainer(ctx, name, registration);
    } catch (error) {
      if (!failed) throw error;
      ctx.log.warn("Docker container cleanup failed; host cleanup remains registered", { name, error: String(error) });
    }
  }
}

async function computeTagsIn(ctx: Context, directory: string): Promise<DockerAPI.ImageTag> {
  const commit = await git.shortCommit(ctx, directory);
  const branch = await git.currentBranch(ctx, directory);
  const dirty = await git.isDirty(ctx, directory);
  const content = await git.filesetHash(ctx, directory);
  const deploy = `commit-${commit}-files-${content}${dirty ? "-dirty" : ""}`;
  return Object.freeze({
    commit,
    branch,
    dirty,
    content,
    deployTag: () => deploy,
    prodTag: () => deploy + "-prod",
    all: () => [deploy, deploy + "-prod"],
  });
}

export const docker = {
  DockerUnavailableError,
  BuildxRequiredError,
  PlatformUnsupportedError,
  build: (ctx: Context, config: DockerAPI.BuildConfig) => build(ctx, config, false),
  buildAndPush: (ctx: Context, config: DockerAPI.BuildConfig) => build(ctx, config, true),
  async push(ctx: Context, image: string, tags: readonly string[], registries: readonly string[]): Promise<void> {
    assertExecution();
    if (!image || !tags.length || !registries.length) throw new Error("Docker push requires an image, tags and registries");
    const colon = image.lastIndexOf(":");
    const base = colon > image.lastIndexOf("/") ? image.slice(0, colon) : image;
    await pushRefs(ctx, image, base, tags, registries);
  },
  async login(ctx: Context, registry: string, username: string, secret: string): Promise<void> {
    assertExecution();
    if (!registry || !username || !secret) throw new Error("Docker login requires registry, username and secret");
    await ctx.mask(secret);
    try {
      await runInput(ctx, ["docker", "login", "--username", username, "--password-stdin", registry], secret);
    } catch (cause) {
      if (unavailable(cause)) throw new DockerUnavailableError("Docker binary is not available on PATH", { cause });
      throw cause;
    }
  },
  buildxPlatforms,
  async filterBuildxPlatforms(ctx: Context, wish: readonly string[]): Promise<string[]> {
    assertExecution();
    if (!wish.length) return [];
    const targets = await buildxPlatforms(ctx);
    return wish.filter((platform) => advertised(platform, targets));
  },
  run,
  computeTags: (ctx: Context) => computeTagsIn(ctx, ctx.run.git.workDir()),
  computeTagsIn,
};
