import type { Args, ArgSpec } from "../index.d.ts";
import { SDKError, errorFields } from "./errors.ts";

export class PlanningDiagnostics {
  readonly #values = new Set<string>();
  readonly #args: Args;
  constructor(specs: readonly ArgSpec[] = [], args: Args = {}) {
    this.#args = args;
    this.declare(specs);
  }
  declare(specs: readonly ArgSpec[]): void {
    for (const spec of specs) {
      if (!spec.secret) continue;
      for (const value of [spec.default, this.#args[spec.name]]) {
        if (!value) continue;
        const values =
          spec.type === "[]string"
            ? [
                value,
                ...value
                  .split(",")
                  .map((part) => part.trim())
                  .filter(Boolean),
              ]
            : [value];
        for (const part of values) {
          this.#values.add(part);
          this.#values.add(JSON.stringify(part).slice(1, -1));
          this.#values.add(Buffer.from(part).toString("base64"));
          try {
            this.#values.add(encodeURIComponent(part));
          } catch {}
        }
      }
    }
  }
  async run<T>(fn: () => T | Promise<T>, prefix = "", forceCode?: string): Promise<T> {
    try {
      return await fn();
    } catch (error) {
      throw this.error(error, prefix, forceCode ?? (error instanceof SDKError ? undefined : "plan_failed"));
    }
  }
  error(error: unknown, prefix = "", forceCode?: string): SDKError {
    try {
      const fields = errorFields(error);
      const values = [...this.#values].filter(Boolean).sort((a, b) => b.length - a.length);
      const pattern = values.length
        ? new RegExp(values.map((value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"), "g")
        : undefined;
      const mask = (value: string): string => (pattern ? value.replace(pattern, "***") : value);
      const sanitize = (value: unknown): unknown => {
        if (typeof value === "string") return mask(value);
        if (Array.isArray(value)) return value.map(sanitize);
        if (value && typeof value === "object")
          return Object.fromEntries(Object.entries(value).map(([key, child]) => [mask(key), sanitize(child)]));
        if ((value === null || typeof value === "number" || typeof value === "boolean") && mask(String(value)) !== String(value))
          return "***";
        return value;
      };
      const safe = sanitize(JSON.parse(JSON.stringify(fields))) as typeof fields;
      return new SDKError(mask(prefix) + safe.message, {
        code: forceCode ?? safe.code,
        ...(safe.hint === undefined ? {} : { hint: safe.hint }),
        ...(safe.details === undefined ? {} : { details: safe.details }),
      });
    } catch {
      return new SDKError("planning diagnostic is not JSON serializable", { code: "plan_failed" });
    }
  }
}
