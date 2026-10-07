// Stdio framing between the engine and an SDK process, per the node
// protocol. The engine writes one JSON request per line to stdin. The SDK
// writes one JSON object per line to stdout: a line with "reply" answers the
// request with that id, and every other line is a log record.

import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import type { LineSink } from "./log.ts";

export type Op = "describe" | "plan" | "run_node" | "run_step" | "eval";

export interface Request {
  id: string;
  op: Op;
  [field: string]: unknown;
}

export type Reply =
  | { reply: string; ok: true; result: unknown }
  | { reply: string; ok: false; error: { message: string } };

export interface InvalidLine {
  id: string | null;
  invalid: string;
}

export function isInvalid(line: Request | InvalidLine): line is InvalidLine {
  return typeof (line as { invalid?: unknown }).invalid === "string" && (line as { op?: unknown }).op === undefined;
}

export async function* readRequests(input: Readable): AsyncGenerator<Request | InvalidLine> {
  const lines = createInterface({ input, crlfDelay: Infinity });
  for await (const line of lines) {
    if (line.trim() === "") continue;
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (err) {
      yield { id: null, invalid: `request is not JSON: ${(err as Error).message}` };
      continue;
    }
    const r = parsed as Partial<Request> | null;
    const id = typeof r?.id === "string" && r.id !== "" ? r.id : null;
    if (id === null || typeof r?.op !== "string") {
      yield { id, invalid: "a request needs a non-empty string id and a string op" };
      continue;
    }
    yield r as Request;
  }
}

export function writeReply(out: LineSink, reply: Reply): void {
  out.write(JSON.stringify(reply) + "\n");
}
