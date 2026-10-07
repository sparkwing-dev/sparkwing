import { PassThrough } from "node:stream";
import { serve, type ServeOptions } from "../src/runner.ts";
import type { NodeRoutes } from "../src/routes.ts";

export class Lines {
  readonly lines: string[] = [];

  write(line: string): void {
    this.lines.push(...line.split("\n").filter((l) => l !== ""));
  }

  json(): Array<Record<string, unknown>> {
    return this.lines.map((l) => JSON.parse(l) as Record<string, unknown>);
  }

  responses(): Array<Record<string, unknown>> {
    return this.json().filter((r) => typeof r["id"] === "number");
  }

  records(): Array<Record<string, unknown>> {
    return this.json().filter((r) => !("id" in r));
  }
}

/** Sends requests to an in-process serve loop and returns everything it wrote. */
export async function converse(requests: unknown[], routes?: NodeRoutes, env: ServeOptions["env"] = {}): Promise<Lines> {
  const input = new PassThrough();
  const out = new Lines();
  const opts: ServeOptions = { input, output: out, env };
  if (routes) opts.routes = routes;
  const done = serve(opts);
  for (const r of requests) input.write(typeof r === "string" ? r + "\n" : JSON.stringify(r) + "\n");
  input.end();
  await done;
  return out;
}
