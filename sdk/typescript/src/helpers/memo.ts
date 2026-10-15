import { createHash } from "node:crypto";
import * as fs from "node:fs/promises";
import * as paths from "node:path";
import type { Context, MemoKeyFn, RepoFilesOption, noMemo as NoMemo } from "../../index.d.ts";
import { assertExecution } from "../scope.ts";
import { globRegex, path, regularFiles } from "./files.ts";

export const noMemo: typeof NoMemo = Symbol("noMemo") as typeof NoMemo;

export function memoKey(...parts: readonly unknown[]): string {
  function canonical(value: unknown): unknown {
    if (value === null || typeof value === "string" || typeof value === "boolean") return value;
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (Array.isArray(value)) return value.map(canonical);
    if (typeof value === "object" && value !== null && Object.getPrototypeOf(value) === Object.prototype) {
      return Object.fromEntries(
        Object.keys(value)
          .sort()
          .map((key) => [key, canonical((value as Record<string, unknown>)[key])]),
      );
    }
    throw new Error("Memo key parts must be JSON serializable");
  }
  return (
    "mk:" +
    createHash("sha256")
      .update(JSON.stringify(canonical(parts)))
      .digest("hex")
  );
}

export function ignore(...patterns: readonly string[]): RepoFilesOption {
  return Object.freeze({ ignore: Object.freeze([...patterns]) });
}

export function inputMatcher(pattern: string): (file: string) => boolean {
  const directory = pattern.endsWith("/");
  const normalized = directory ? pattern.slice(0, -1) : pattern;
  const expression = globRegex(normalized, true);
  if (directory && !normalized.includes("/"))
    return (file) =>
      file
        .split("/")
        .slice(0, -1)
        .some((part) => expression.test(part));
  if (directory)
    return (file) =>
      file
        .split("/")
        .slice(0, -1)
        .some((_, i, parts) => expression.test(parts.slice(0, i + 1).join("/")));
  if (!pattern.includes("/")) return (file) => expression.test(paths.posix.basename(file));
  return (file) => expression.test(file);
}

function tracked(select: (file: string) => boolean): MemoKeyFn {
  return async (ctx) => {
    assertExecution();
    ctx.signal.throwIfAborted();
    const root = await ctx.exec(["git", "rev-parse", "--show-toplevel"]).string();
    const result = await ctx.exec(["git", "ls-files", "-z"], { cwd: root }).capture();
    const names = [...new Set(result.stdout.split("\0").filter(Boolean))].filter(select).sort();
    return hashFiles(root, names, ctx);
  };
}

export function files(...globs: readonly string[]): MemoKeyFn {
  const matches = globs.map(inputMatcher);
  return tracked((file) => matches.some((match) => match(file)));
}

export function repoFiles(...options: readonly RepoFilesOption[]): MemoKeyFn {
  const matches = options.flatMap((option) => option.ignore).map(inputMatcher);
  return tracked((file) => !matches.some((match) => match(file)));
}

async function hashFiles(root: string, names: readonly string[], ctx: Context, tracked = true): Promise<string> {
  const hash = createHash("sha256");
  const part = (bytes: Uint8Array): void => {
    const length = Buffer.alloc(8);
    length.writeBigUInt64BE(BigInt(bytes.length));
    hash.update(length).update(bytes);
  };
  for (const name of names) {
    ctx.signal.throwIfAborted();
    const file = paths.join(root, name);
    const info = await fs.lstat(file);
    if (!tracked && !info.isFile()) throw new Error(`Tree entry is not a regular file: ${name}`);
    if (!info.isSymbolicLink() && !info.isFile()) throw new Error(`Unsupported memo entry: ${name}`);
    const bytes = info.isSymbolicLink() ? await fs.readlink(file, { encoding: "buffer" }) : await fs.readFile(file);
    part(Buffer.from(name));
    if (tracked) part(Buffer.from([info.isSymbolicLink() ? 1 : 0]));
    part(bytes);
  }
  return "memo:" + hash.digest("hex");
}

export function tree(root: string): MemoKeyFn {
  return async (ctx) => {
    assertExecution();
    const absolute = paths.resolve(path(root));
    if ((await fs.realpath(absolute)) !== absolute) throw new Error(`Tree root traverses a symlink: ${root}`);
    return hashFiles(absolute, await regularFiles(absolute, ctx.signal), ctx, false);
  };
}

export function env(...names: readonly string[]): MemoKeyFn {
  const sorted = [...names].sort();
  return () => {
    assertExecution();
    return memoKey(sorted.map((name) => [name, process.env[name] ?? null]));
  };
}

export function constant(value: string): MemoKeyFn {
  if (!value) throw new Error("Memo constant must not be empty");
  return () => value;
}

export function compose(...inputs: readonly MemoKeyFn[]): MemoKeyFn {
  return async (ctx) => {
    const keys: string[] = [];
    for (const input of inputs) {
      const key = await input(ctx);
      if (key === noMemo) return noMemo;
      if (!key) throw new Error("Memo input returned an empty key");
      keys.push(key);
    }
    return memoKey(...keys);
  };
}
