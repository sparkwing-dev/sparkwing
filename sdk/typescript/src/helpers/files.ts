import * as fs from "node:fs/promises";
import * as paths from "node:path";
import { assertExecution, projectRoot } from "../scope.ts";
import { NoProjectError } from "../errors.ts";

export function workDir(): string {
  return projectRoot();
}

export function path(...parts: readonly string[]): string {
  if (parts.length && paths.isAbsolute(parts[0]!)) return paths.join(...parts);
  const root = workDir();
  if (!root) throw new NoProjectError("No pipeline project root is available");
  return paths.join(root, ...parts);
}

export async function readFile(file: string): Promise<Uint8Array> {
  assertExecution();
  return fs.readFile(path(file));
}

export async function writeFile(file: string, data: Uint8Array): Promise<void> {
  assertExecution();
  await fs.writeFile(path(file), data, { mode: 0o644 });
}

export function globRegex(pattern: string, recursive = false): RegExp {
  let result = "^";
  for (let i = 0; i < pattern.length; i++) {
    const char = pattern[i]!;
    if (char === "*") {
      if (recursive && pattern[i + 1] === "*") {
        i++;
        if (pattern[i + 1] === "/") {
          result += "(?:.*/)?";
          i++;
        } else result += ".*";
      } else result += "[^/]*";
    } else if (char === "?") result += "[^/]";
    else if (char === "[") {
      let end = i + 1;
      if (pattern[end] === "!" || pattern[end] === "^") end++;
      while (end < pattern.length && pattern[end] !== "]") end++;
      if (end >= pattern.length || end === i + 1) throw new Error(`Invalid glob pattern: ${pattern}`);
      let content = pattern.slice(i + 1, end);
      if (content.startsWith("!")) content = "^" + content.slice(1);
      result += "[" + content.replaceAll("\\", "\\\\") + "]";
      i = end;
    } else if (char === "\\") {
      if (++i === pattern.length) throw new Error(`Invalid glob pattern: ${pattern}`);
      result += escapeRegex(pattern[i]!);
    } else result += escapeRegex(char);
  }
  try {
    return new RegExp(result + "$");
  } catch (cause) {
    throw new Error(`Invalid glob pattern: ${pattern}`, { cause });
  }
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export async function glob(pattern: string): Promise<string[]> {
  assertExecution();
  const absolute = path(pattern);
  const root = paths.parse(absolute).root;
  const parts = absolute.slice(root.length).split(paths.sep).filter(Boolean);
  const matchers = parts.map((part) => globRegex(part));
  let candidates = [root];
  for (let i = 0; i < parts.length; i++) {
    const next: string[] = [];
    for (const directory of candidates) {
      let entries;
      try {
        entries = await fs.readdir(directory);
      } catch (error) {
        if (isMissing(error) || (error as NodeJS.ErrnoException).code === "ENOTDIR") continue;
        throw error;
      }
      for (const name of entries) if (matchers[i]!.test(name)) next.push(paths.join(directory, name));
    }
    candidates = next;
  }
  return candidates.sort();
}

export function isMissing(error: unknown): boolean {
  return typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT";
}

export async function regularFiles(root: string, signal?: AbortSignal, skip: ReadonlySet<string> = new Set()): Promise<string[]> {
  if (!(await fs.stat(root)).isDirectory()) throw new Error(`Not a directory: ${root}`);
  const files: string[] = [];
  async function visit(relative: string): Promise<void> {
    signal?.throwIfAborted();
    const entries = await fs.readdir(paths.join(root, relative), { withFileTypes: true });
    for (const entry of entries) {
      const name = paths.join(relative, entry.name);
      if (entry.isDirectory() && !skip.has(entry.name)) await visit(name);
      else if (entry.isFile()) files.push(name.split(paths.sep).join("/"));
    }
  }
  await visit("");
  return files.sort();
}
