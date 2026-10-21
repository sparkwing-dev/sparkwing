import type { Args, ArgSpec } from "../index.d.ts";
import { SDKError, errorFields } from "./errors.ts";

type Span = { lo: number; hi: number };

// Mirrors internal/secrets; the shared vectors pin both implementations.
export class Masker {
  readonly #values = new Set<string>();
  readonly #keys = new Set<string>();
  register(value: string): void {
    this.#register(value, /[,\n]/, this.#values);
    this.#register(value, /,/, this.#keys);
  }
  #register(value: string, separators: RegExp, values: Set<string>): void {
    if (!value) return;
    const parts = [
      value,
      ...value
        .split(separators)
        .map((part) => (value.includes(",") ? part.trim() : part.replace(/\r$/, "")))
        .filter((part) => part.trim()),
    ];
    for (const part of parts) {
      values.add(part);
      const json = JSON.stringify(part).slice(1, -1);
      values.add(json);
      const goQuoted = json
        .replace(/\\u00(0[0-9a-f]|1[0-9a-f])/gi, (_, hex: string) =>
          hex.toLowerCase() === "07" ? "\\a" : hex.toLowerCase() === "0b" ? "\\v" : `\\x${hex.toLowerCase()}`,
        )
        .replace(/\x7f/g, "\\x7f");
      values.add(goQuoted);
      values.add(
        goQuoted.replace(/[^\x00-\x7F]/gu, (c) => {
          const cp = c.codePointAt(0)!;
          return cp > 0xffff ? `\\U${cp.toString(16).padStart(8, "0")}` : `\\u${cp.toString(16).padStart(4, "0")}`;
        }),
      );
      values.add(json.replace(/[<>&\u2028\u2029]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`));
      const bytes = Buffer.from(part);
      const uri = Array.from(bytes, (byte) => {
        const c = String.fromCharCode(byte);
        return /[A-Za-z0-9\-_.~!'()*]/.test(c) ? c : `%${byte.toString(16).toUpperCase().padStart(2, "0")}`;
      }).join("");
      const query = uri.replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
      const forms = [
        query,
        query.replace(/%20/g, "+"),
        query.replace(/%(24|26|2B|3A|3D|40)/g, (c) => decodeURIComponent(c)),
        uri,
        bytes.toString("base64"),
        bytes.toString("base64url"),
      ];
      for (let lead = 0; lead < 3; lead++) {
        const encoded = Buffer.concat([Buffer.alloc(lead), bytes]).toString("base64");
        const inside = encoded.slice(Math.ceil((8 * lead) / 6), Math.floor((8 * (lead + bytes.length)) / 6));
        forms.push(inside, inside.replace(/\+/g, "-").replace(/\//g, "_"));
      }
      for (const form of forms) if (form.length >= 4) values.add(form);
    }
  }
  #spans(text: string, values = this.#values): Span[] {
    const spans: Span[] = [];
    let decoded = "";
    const units: Span[] = [];
    for (let i = 0; i < text.length;) {
      const match = /^\\(?:u[\da-fA-F]{4}|["\\/bfnrt])/.exec(text.slice(i));
      const raw = match?.[0] ?? text[i]!;
      const value = match ? (JSON.parse(`"${raw}"`) as string) : raw;
      decoded += value;
      for (let j = 0; j < value.length; j++) units.push({ lo: i, hi: i + raw.length });
      i += raw.length;
    }
    for (const value of values) {
      for (const [source, escaped] of [
        [text, false],
        [decoded, true],
      ] as const) {
        for (let at = 0; (at = source.indexOf(value, at)) >= 0; at += value.length) {
          spans.push(escaped ? { lo: units[at]!.lo, hi: units[at + value.length - 1]!.hi } : { lo: at, hi: at + value.length });
        }
      }
    }
    spans.sort((a, b) => a.lo - b.lo);
    const merged: Span[] = [];
    for (const span of spans) {
      const last = merged.at(-1);
      if (last && span.lo < last.hi) last.hi = Math.max(last.hi, span.hi);
      else merged.push({ ...span });
    }
    return merged;
  }
  containsSecret(text: string): boolean {
    return this.#spans(text).length > 0;
  }
  mask(text: string): string {
    return this.#mask(text, this.#values);
  }
  #mask(text: string, values: Set<string>): string {
    for (let pass = 0; pass < 4; pass++) {
      const spans = this.#spans(text, values);
      if (!spans.length) break;
      let result = "",
        previous = 0;
      for (const span of spans) {
        result += text.slice(previous, span.lo) + "***";
        previous = span.hi;
      }
      text = result + text.slice(previous);
    }
    return this.#spans(text, values).length ? "" : text;
  }
  maskJSON(value: unknown): unknown {
    const visit = (value: unknown): unknown => {
      if (typeof value === "string") return this.mask(value);
      if (Array.isArray(value)) return value.map(visit);
      if (value && typeof value === "object")
        return Object.fromEntries(Object.entries(value).map(([key, child]) => [this.#mask(key, this.#keys), visit(child)]));
      const text = JSON.stringify(value);
      return text !== undefined && this.mask(text) !== text ? this.mask(text) : value;
    };
    return visit(JSON.parse(JSON.stringify(value)));
  }
}

export class PlanningDiagnostics {
  readonly #masker = new Masker();
  readonly #args: Args;
  constructor(specs: readonly ArgSpec[] = [], args: Args = {}) {
    this.#args = args;
    this.declare(specs);
  }
  declare(specs: readonly ArgSpec[]): void {
    for (const spec of specs) {
      if (!spec.secret) continue;
      for (const value of [spec.default, this.#args[spec.name]]) if (value) this.#masker.register(value);
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
      return new SDKError(this.#masker.mask(prefix + fields.message), {
        code: this.#masker.mask(forceCode ?? fields.code),
        ...(fields.hint === undefined ? {} : { hint: this.#masker.mask(fields.hint) }),
        ...(fields.details === undefined ? {} : { details: this.#masker.maskJSON(fields.details) as Record<string, unknown> }),
      });
    } catch {
      return new SDKError("planning diagnostic is not JSON serializable", { code: "plan_failed" });
    }
  }
}
