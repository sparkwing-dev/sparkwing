// Stdio framing between the engine and an SDK process. The engine writes one
// JSON request per line to stdin. The SDK writes one JSON object per line to
// stdout: a line with a numeric "id" answers the request with that id, and
// every other line is a log record.

import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import type { LineSink } from "./log.ts";

export type Method = "describe" | "plan" | "run_node" | "run_step" | "call" | "shutdown";

export interface Request {
  id: number;
  method: Method;
  params?: Record<string, unknown>;
}

export interface ProtocolError {
  code: "bad_request" | "unknown_method" | "unknown_pipeline" | "unknown_node" | "unknown_step" | "unknown_closure" | "internal";
  message: string;
}

export interface Response {
  id: number;
  result?: unknown;
  error?: ProtocolError;
}

export async function* readRequests(input: Readable): AsyncGenerator<Request | { id: number | null; invalid: string }> {
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
    const r = parsed as Partial<Request>;
    if (typeof r !== "object" || r === null || typeof r.id !== "number" || typeof r.method !== "string") {
      yield { id: typeof r?.id === "number" ? r.id : null, invalid: "request needs a numeric id and a string method" };
      continue;
    }
    yield r as Request;
  }
}

export function writeResponse(out: LineSink, res: Response): void {
  out.write(JSON.stringify(res) + "\n");
}
