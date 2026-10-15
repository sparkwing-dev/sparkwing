import { AsyncLocalStorage } from "node:async_hooks";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { NoProjectError, failure } from "./errors.ts";

interface Scope {
  phase: "plan" | "execute";
  root?: string;
}
const current = new AsyncLocalStorage<Scope>();
export function runScope<T>(scope: Scope, fn: () => T): T {
  return current.run(scope, fn);
}
export function assertExecution(): void {
  if (current.getStore()?.phase === "plan") throw failure("plan_failed", "side effects are unavailable during planning");
}
export function projectRoot(): string {
  const root = current.getStore()?.root;
  if (root !== undefined) return root;
  let dir = process.cwd();
  for (;;) {
    if (existsSync(join(dir, ".sparkwing"))) return dir;
    const parent = dirname(dir);
    if (parent === dir) throw new NoProjectError("no .sparkwing project found");
    dir = parent;
  }
}
