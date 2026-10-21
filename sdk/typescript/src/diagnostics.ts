import type { Args, ArgSpec } from "../index.d.ts";
import { SDKError, errorFields } from "./errors.ts";

function trimFragment(value: string): string {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
}

function listFragments(value: string): string[] {
  return value
    .split(",")
    .map(trimFragment)
    .filter((part) => Buffer.byteLength(part) >= 12);
}

type Span = { lo: number; hi: number };

// Mirrors internal/secrets; the shared vectors pin both implementations.
export class Masker {
  readonly #values = new Set<string>();
  readonly #raw = new Set<string>();
  register(value: string): void {
    this.#register(value, false);
  }
  registerList(value: string): void {
    this.#register(value, true);
  }
  merge(other: Masker): void {
    for (const value of other.#raw) this.#raw.add(value);
    for (const value of other.#values) this.#values.add(value);
  }
  #register(value: string, list: boolean): void {
    const registration = JSON.stringify([value, list]);
    if (!value || this.#raw.has(registration)) return;
    this.#raw.add(registration);
    const parts = [value];
    if (list) parts.push(...listFragments(value));
    for (const part of [...parts])
      for (const line of part.split("\n")) {
        const fragment = line.replace(/\r$/, "");
        if (trimFragment(fragment) && Buffer.byteLength(fragment) >= 12) parts.push(fragment);
      }
    const values = this.#values;
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
        for (let at = 0; (at = source.indexOf(value, at)) >= 0; at++) {
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
  #marker(): string {
    return this.containsSecret("***") ? "" : "***";
  }
  mask(text: string): string {
    const marker = this.#marker();
    for (;;) {
      const spans = this.#spans(text);
      if (!spans.length) return text;
      let result = "",
        previous = 0;
      for (const span of spans) {
        result += text.slice(previous, span.lo) + marker;
        previous = span.hi;
      }
      text = result + text.slice(previous);
    }
  }
  maskJSON(value: unknown): unknown {
    const visit = (value: unknown): unknown => {
      if (typeof value === "string") return this.mask(value);
      if (Array.isArray(value)) return value.map(visit);
      if (value && typeof value === "object") {
        const out: Record<string, unknown> = {};
        for (const [key, child] of Object.entries(value).sort(([a], [b]) => Buffer.compare(Buffer.from(a), Buffer.from(b)))) {
          const masked = this.mask(key);
          if (Object.hasOwn(out, masked)) return this.#marker();
          Object.defineProperty(out, masked, { value: visit(child), enumerable: true });
        }
        return out;
      }
      const text = JSON.stringify(value);
      return text !== undefined && this.mask(text) !== text ? this.mask(text) : value;
    };
    return visit(JSON.parse(JSON.stringify(value)));
  }
}

export class PlanningDiagnostics {
  readonly #masker = new Masker();
  readonly #args: Args;
  readonly #pending = new Set<string>();
  readonly #register: ((value: string) => Promise<void>) | undefined;
  constructor(specs: readonly ArgSpec[] = [], args: Args = {}, register?: (value: string) => Promise<void>) {
    this.#register = register;
    this.#args = args;
    this.declare(specs);
  }
  declare(specs: readonly ArgSpec[]): void {
    for (const spec of specs) {
      if (!spec.secret) continue;
      for (const value of [spec.default, this.#args[spec.name]])
        if (value) {
          if (spec.type === "[]string") {
            this.#masker.registerList(value);
            for (const part of listFragments(value)) this.#pending.add(part);
          } else this.#masker.register(value);
        }
    }
  }
  async run<T>(fn: () => T | Promise<T>, prefix = "", forceCode?: string, fallbackCode = "plan_failed"): Promise<T> {
    try {
      await this.#flush();
      const result = await fn();
      await this.#flush();
      return result;
    } catch (error) {
      throw this.error(error, prefix, forceCode ?? (error instanceof SDKError ? undefined : fallbackCode), fallbackCode);
    }
  }
  async #flush(): Promise<void> {
    if (!this.#register) return;
    for (const value of this.#pending) {
      await this.#register(value);
      this.#pending.delete(value);
    }
  }
  error(error: unknown, prefix = "", forceCode?: string, fallbackCode = "plan_failed"): SDKError {
    try {
      const fields = errorFields(error);
      const details = fields.details === undefined ? undefined : this.#masker.maskJSON(fields.details);
      return new SDKError(this.#masker.mask(prefix + fields.message), {
        code: this.#masker.containsSecret(forceCode ?? fields.code) ? fallbackCode : (forceCode ?? fields.code),
        ...(fields.hint === undefined ? {} : { hint: this.#masker.mask(fields.hint) }),
        ...(details && typeof details === "object" && !Array.isArray(details) ? { details: details as Record<string, unknown> } : {}),
      });
    } catch {
      return new SDKError("planning diagnostic is not JSON serializable", { code: fallbackCode });
    }
  }
}
