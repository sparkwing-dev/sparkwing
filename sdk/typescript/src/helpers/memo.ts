import type { MemoInput, RepoFilesOption, noMemo as NoMemo } from "../../index.d.ts";

export const noMemo: typeof NoMemo = Symbol("noMemo") as typeof NoMemo;

export function memoKey(...parts: readonly unknown[]): readonly unknown[] | typeof noMemo {
  return parts.includes(noMemo) ? noMemo : parts;
}
export function ignore(...patterns: readonly string[]): RepoFilesOption {
  return { ignore: patterns };
}
export function files(...patterns: readonly string[]): MemoInput {
  return { kind: "files", patterns };
}
export function repoFiles(...options: readonly RepoFilesOption[]): MemoInput {
  return { kind: "repo_files", patterns: options.flatMap((option) => option.ignore) };
}
export function tree(root: string): MemoInput {
  return { kind: "tree", root };
}
export function env(...names: readonly string[]): MemoInput {
  return { kind: "env", names };
}
export function constant(value: string): MemoInput {
  return { kind: "const", value };
}
export function compose(...inputs: readonly MemoInput[]): MemoInput {
  return { kind: "compose", inputs };
}

export function parts(...values: readonly unknown[]): MemoInput {
  return { kind: "parts", parts: values };
}
