import type { Context, docker as DockerAPI } from "../../index.d.ts";
import { helper } from "./host.ts";
export class DockerUnavailableError extends Error {}
export class BuildxRequiredError extends Error {}
export class PlatformUnsupportedError extends Error {}
async function call<T>(ctx: Context, op: string, args: readonly unknown[]): Promise<T> {
  try {
    return await helper<T>(ctx, "docker." + op, args);
  } catch (error) {
    const code = (error as { code?: string }).code;
    const Type =
      code === "docker: binary not available on PATH"
        ? DockerUnavailableError
        : code === "docker: buildx plugin required for multi-platform builds"
          ? BuildxRequiredError
          : code === "docker: requested platform not supported by active buildx builder"
            ? PlatformUnsupportedError
            : undefined;
    if (Type) throw new Type(String(error), { cause: error });
    throw error;
  }
}
async function computeTagsIn(ctx: Context, directory: string): Promise<DockerAPI.ImageTag> {
  const data = await call<{ commit: string; content: string; branch: string; dirty: boolean }>(ctx, "ComputeTagsIn", [directory]);
  const deploy = `commit-${data.commit}-files-${data.content}${data.dirty ? "-dirty" : ""}`;
  return Object.freeze({ ...data, deployTag: () => deploy, prodTag: () => deploy + "-prod", all: () => [deploy, deploy + "-prod"] });
}
export const docker = {
  DockerUnavailableError,
  BuildxRequiredError,
  PlatformUnsupportedError,
  build: (ctx: Context, config: DockerAPI.BuildConfig) => call<DockerAPI.BuildResult>(ctx, "Build", [config]),
  buildAndPush: (ctx: Context, config: DockerAPI.BuildConfig) => call<DockerAPI.BuildResult>(ctx, "BuildAndPush", [config]),
  push: (ctx: Context, image: string, tags: readonly string[], registries: readonly string[]) =>
    call<void>(ctx, "Push", [image, tags, registries]),
  login: (ctx: Context, registry: string, username: string, secret: string) => call<void>(ctx, "Login", [registry, username, secret]),
  buildxPlatforms: (ctx: Context) => call<string[]>(ctx, "BuildxPlatforms", []),
  filterBuildxPlatforms: (ctx: Context, wish: readonly string[]) => call<string[]>(ctx, "FilterBuildxPlatforms", [wish]),
  async run(ctx: Context, options: DockerAPI.RunOptions): Promise<void> {
    const { stdout, stderr, ...opts } = options;
    let error: unknown;
    let out: { stdout: string; stderr: string };
    try {
      out = await call(ctx, "Run", [{ ...opts, captureOutput: !!stdout || !!stderr }]);
    } catch (cause) {
      error = cause;
      out = (cause as { result?: { stdout: string; stderr: string } }).result ?? { stdout: "", stderr: "" };
    }

    if (stdout) stdout(Buffer.from(out.stdout, "base64"));
    else if (out.stdout) ctx.log.info(Buffer.from(out.stdout, "base64").toString("utf8"));
    if (stderr) stderr(Buffer.from(out.stderr, "base64"));
    else if (out.stderr) ctx.log.info(Buffer.from(out.stderr, "base64").toString("utf8"));
    if (error) throw error;
  },
  computeTags: (ctx: Context) => computeTagsIn(ctx, ctx.run.git.workDir()),
  computeTagsIn,
};
