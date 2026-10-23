import { createHash } from "node:crypto";
import * as fs from "node:fs/promises";
import * as paths from "node:path";
import type { Context, GitInfo, git as GitAPI } from "../../index.d.ts";
import { assertExecution } from "../scope.ts";
import { isMissing, path, regularFiles, inputMatcher } from "./files.ts";

async function command(ctx: Context, directory: string, ...args: string[]): Promise<string> {
  assertExecution();
  const result = await ctx
    .exec(["git", ...args], {
      cwd: directory || path(),
      env: { GIT_TERMINAL_PROMPT: "0", GCM_INTERACTIVE: "0" },
    })
    .capture();
  return result.stdout;
}

function exitCode(error: unknown): number | undefined {
  return typeof error === "object" && error !== null && "exitCode" in error ? Number(error.exitCode) : undefined;
}

async function optionalRef(ctx: Context, directory: string, ref: string): Promise<string> {
  try {
    return (await command(ctx, directory, "symbolic-ref", "--quiet", "--short", ref)).trim();
  } catch (error) {
    if (exitCode(error) !== 1) throw error;
    await command(ctx, directory, "rev-parse", "--git-dir");
    return "";
  }
}

async function currentSHA(ctx: Context, directory: string): Promise<string> {
  return (await command(ctx, directory, "rev-parse", "HEAD")).trim();
}

async function remoteOriginURL(ctx: Context, directory: string): Promise<string> {
  const remotes = (await command(ctx, directory, "remote")).trim().split("\n");
  if (!remotes.includes("origin")) return "";
  return (await command(ctx, directory, "remote", "get-url", "origin")).trim();
}

async function hasRepository(directory: string): Promise<boolean> {
  let current = paths.resolve(directory);
  for (;;) {
    try {
      await fs.lstat(paths.join(current, ".git"));
      return true;
    } catch (error) {
      if (!isMissing(error)) throw error;
    }
    const parent = paths.dirname(current);
    if (parent === current) return false;
    current = parent;
  }
}

async function filesetHash(ctx: Context, directory: string): Promise<string> {
  assertExecution();
  const root = path(directory || ".");
  const names = (await hasRepository(root))
    ? (await command(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")).split("\0").filter(Boolean)
    : await regularFiles(root, ctx.signal, new Set([".git", "node_modules", "vendor", "dist", "build", ".sparkwing"]));
  let ignores: ((file: string) => boolean)[] = [];
  try {
    ignores = (await fs.readFile(paths.join(root, ".dockerignore"), "utf8"))
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"))
      .map((pattern) => {
        const matches = inputMatcher(pattern);
        return (file: string) => matches(file) || file.startsWith(pattern + "/");
      });
  } catch (error) {
    if (!isMissing(error)) throw error;
  }
  const hash = createHash("sha256");
  for (const name of [...new Set(names)].sort()) {
    ctx.signal.throwIfAborted();
    if (ignores.some((ignore) => ignore(name))) continue;
    const file = paths.join(root, name);
    let stat;
    try {
      stat = await fs.stat(file);
    } catch (error) {
      if (isMissing(error)) continue;
      throw error;
    }
    if (!stat.isFile()) continue;
    const bytes = await fs.readFile(file);
    hash.update(`${Buffer.byteLength(name)}\0${name}\0${stat.mode & 0o777}\0${bytes.length}\0`).update(bytes);
  }
  return names.length ? hash.digest("hex").slice(0, 12) : "";
}

interface Version {
  numbers: bigint[];
  prerelease: string[];
}

function version(value: string): Version | undefined {
  const match =
    /^v?(0|[1-9]\d*)(?:\.(0|[1-9]\d*))?(?:\.(0|[1-9]\d*))?(?:-([\da-zA-Z-]+(?:\.[\da-zA-Z-]+)*))?(?:\+([\da-zA-Z-]+(?:\.[\da-zA-Z-]+)*))?$/.exec(
      value,
    );
  if (!match || ((match[4] || match[5]) && !match[3])) return undefined;
  const prerelease = match[4]?.split(".") ?? [];
  if (prerelease.some((part) => /^0\d+$/.test(part))) return undefined;
  return { numbers: [BigInt(match[1]!), BigInt(match[2] ?? 0), BigInt(match[3] ?? 0)], prerelease };
}

function compare(a: Version, b: Version): number {
  for (let i = 0; i < 3; i++) if (a.numbers[i] !== b.numbers[i]) return a.numbers[i]! > b.numbers[i]! ? 1 : -1;
  if (!a.prerelease.length || !b.prerelease.length) return Number(!a.prerelease.length) - Number(!b.prerelease.length);
  for (let i = 0; i < Math.min(a.prerelease.length, b.prerelease.length); i++) {
    const left = a.prerelease[i]!,
      right = b.prerelease[i]!;
    if (left === right) continue;
    const leftNumber = /^\d+$/.test(left),
      rightNumber = /^\d+$/.test(right);
    if (leftNumber !== rightNumber) return leftNumber ? -1 : 1;
    return leftNumber ? (BigInt(left) > BigInt(right) ? 1 : -1) : left > right ? 1 : -1;
  }
  return a.prerelease.length - b.prerelease.length;
}

async function latestTag(ctx: Context, directory: string, prefix: string): Promise<string> {
  const tags = (await command(ctx, directory, "tag", "--list")).trim().split("\n");
  let best = "",
    bestVersion: Version | undefined;
  for (const tag of tags) {
    if (!tag.startsWith(prefix)) continue;
    const candidate = version(tag.slice(prefix.length));
    if (candidate && (!bestVersion || compare(candidate, bestVersion) > 0)) {
      best = tag;
      bestVersion = candidate;
    }
  }
  return best;
}

async function validateTag(ctx: Context, directory: string, tag: string): Promise<void> {
  if (!tag || tag.startsWith("-")) throw new Error("Git tag must be a valid nonempty tag name");
  await command(ctx, directory, "check-ref-format", `refs/tags/${tag}`);
}

async function tagExistsOnRemote(ctx: Context, directory: string, tag: string): Promise<boolean> {
  await validateTag(ctx, directory, tag);
  const output = await command(ctx, directory, "ls-remote", "--tags", "origin", `refs/tags/${tag}`);
  return output.split("\n").some((line) => line.split("\t")[1] === `refs/tags/${tag}`);
}

async function pushTag(ctx: Context, directory: string, tag: string, message: string): Promise<void> {
  if (await tagExistsOnRemote(ctx, directory, tag)) throw new Error(`Git tag already exists on remote: ${tag}`);
  let local: string | undefined;
  try {
    local = (await command(ctx, directory, "rev-parse", "--verify", "--quiet", `refs/tags/${tag}^{commit}`)).trim();
  } catch (error) {
    if (exitCode(error) !== 1) throw error;
  }
  if (local !== undefined && local !== (await currentSHA(ctx, directory))) throw new Error(`Local git tag points outside HEAD: ${tag}`);
  if (local === undefined) await command(ctx, directory, "tag", "-a", tag, "-m", message || tag);
  await command(ctx, directory, "push", "origin", `refs/tags/${tag}`);
}

export const git = {
  async clone(ctx: Context, url: string, destDir: string, options: GitAPI.CloneOptions = {}): Promise<void> {
    const args = ["clone"];
    if (options.depth !== undefined) {
      if (!Number.isInteger(options.depth) || options.depth <= 0) throw new Error("Clone depth must be positive");
      args.push("--depth", String(options.depth));
    }
    await command(ctx, "", ...args, "--", url, destDir);
  },
  async fetch(ctx: Context, directory: string): Promise<void> {
    await command(ctx, directory, "fetch");
  },
  async shortCommit(ctx: Context, directory: string): Promise<string> {
    return (await currentSHA(ctx, directory)).slice(0, 12);
  },
  currentSHA,
  currentBranch: (ctx: Context, directory: string) => optionalRef(ctx, directory, "HEAD"),
  async defaultBranch(ctx: Context, directory: string): Promise<string> {
    return (await optionalRef(ctx, directory, "refs/remotes/origin/HEAD")).replace(/^origin\//, "");
  },
  remoteOriginURL,
  async isDirty(ctx: Context, directory: string): Promise<boolean> {
    return (await command(ctx, directory, "status", "--porcelain")).trim() !== "";
  },
  filesetHash,
  async changedFiles(ctx: Context, directory: string, since: string): Promise<string[]> {
    return (await command(ctx, directory, "diff", "--name-only", "-z", ...(since ? ["--end-of-options", `${since}...HEAD`] : ["HEAD"])))
      .split("\0")
      .filter(Boolean);
  },
  async tagsAtHead(ctx: Context, directory: string): Promise<string[]> {
    return (await command(ctx, directory, "tag", "--points-at", "HEAD")).trim().split("\n").filter(Boolean);
  },
  latestTag,
  tagExistsOnRemote,
  pushTag,
};

export class Git implements GitInfo {
  readonly repo: string;
  readonly branch: string;
  readonly commit: string;
  readonly defaultBranch?: string;
  readonly repoURL?: string;
  #directory: string;
  constructor(directory: string, facts: GitInfo) {
    this.#directory = directory;
    this.repo = facts.repo;
    this.branch = facts.branch;
    this.commit = facts.commit;
    if (facts.defaultBranch !== undefined) this.defaultBranch = facts.defaultBranch;
    if (facts.repoURL !== undefined) this.repoURL = facts.repoURL;
    Object.freeze(this);
  }
  workDir(): string {
    return this.#directory;
  }
  shortSHA(): string {
    return this.commit.slice(0, 12);
  }
  name(): string {
    return this.repo.split("/").at(-1) ?? "";
  }
  isDirty(ctx: Context): Promise<boolean> {
    return git.isDirty(ctx, this.#directory);
  }
  filesetHash(ctx: Context): Promise<string> {
    return git.filesetHash(ctx, this.#directory);
  }
  changedFiles(ctx: Context, since: string): Promise<string[]> {
    return git.changedFiles(ctx, this.#directory, since);
  }
  tagsAtHead(ctx: Context): Promise<string[]> {
    return git.tagsAtHead(ctx, this.#directory);
  }
  latestTag(ctx: Context, prefix: string): Promise<string> {
    return git.latestTag(ctx, this.#directory, prefix);
  }
  pushTag(ctx: Context, tag: string, message: string): Promise<void> {
    return git.pushTag(ctx, this.#directory, tag, message);
  }
}

export async function gitFromTree(ctx: Context, directory: string): Promise<Git> {
  const root = path(directory || ".");
  const commit = await git.currentSHA(ctx, root);
  const branch = await git.currentBranch(ctx, root);
  const defaultBranch = await git.defaultBranch(ctx, root);
  const repoURL = await git.remoteOriginURL(ctx, root);
  const match = /^(?:git@github\.com:|https?:\/\/github\.com\/)([^/]+)\/([^/]+?)(?:\.git)?\/?$/.exec(repoURL);
  return new Git(root, { commit, branch, defaultBranch, repoURL, repo: match ? `${match[1]}/${match[2]}` : "" });
}

export function githubOwnerRepo(slug: string): { owner: string; repo: string } {
  const match = /^([^/]+)\/(.+)$/.exec(slug.trim());
  return match ? { owner: match[1]!, repo: match[2]! } : { owner: "", repo: "" };
}
