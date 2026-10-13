import { PassThrough } from "node:stream";
import { serve, type ServeOptions } from "../src/runner.ts";
import type { NodeRoutes } from "../src/routes.ts";

/** A request as a test writes it: an id and whatever else the case needs. */
export type Req = { id: string; [field: string]: unknown };

export class Lines {
  readonly lines: string[] = [];
  #waiters: Array<() => void> = [];

  write(line: string): void {
    this.lines.push(...line.split("\n").filter((l) => l !== ""));
    const waiters = this.#waiters;
    this.#waiters = [];
    for (const w of waiters) w();
  }

  json(): Array<Record<string, unknown>> {
    return this.lines.map((l) => JSON.parse(l) as Record<string, unknown>);
  }

  replies(): Array<Record<string, unknown>> {
    return this.json().filter((r) => "reply" in r);
  }

  reply(id: string): Record<string, unknown> | undefined {
    return this.replies().find((r) => r["reply"] === id);
  }

  records(): Array<Record<string, unknown>> {
    return this.json().filter((r) => !("reply" in r));
  }

  /** Resolves with the reply to id once the runner writes it. */
  async waitReply(id: string): Promise<Record<string, unknown>> {
    for (;;) {
      const r = this.reply(id);
      if (r) return r;
      await new Promise<void>((resolve) => this.#waiters.push(resolve));
    }
  }
}

/** An in-process runner loop, driven the way the engine drives one. */
export class Conversation {
  readonly out = new Lines();
  readonly #input = new PassThrough();
  readonly done: Promise<void>;

  constructor(routes?: NodeRoutes) {
    const opts: ServeOptions = { input: this.#input, output: this.out };
    if (routes) opts.routes = routes;
    this.done = serve(opts);
  }

  /** Writes a request without waiting for its reply. */
  write(req: unknown): void {
    this.#input.write(typeof req === "string" ? req + "\n" : JSON.stringify(req) + "\n");
  }

  /** Writes a request and returns its reply. */
  async send(req: Req): Promise<Record<string, unknown>> {
    this.write(req);
    return this.out.waitReply(req.id);
  }

  async end(): Promise<void> {
    this.#input.end();
    await this.done;
  }
}

/** Sends each request after the previous reply, then ends the request pipe. */
export async function converse(requests: Array<Req>, routes?: NodeRoutes): Promise<Lines> {
  const c = new Conversation(routes);
  for (const r of requests) await c.send(r);
  await c.end();
  return c.out;
}
