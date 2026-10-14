// Framing between the engine and an SDK process, per the node protocol. The
// engine writes one JSON request per line to the request pipe. The SDK writes
// one JSON object per line to the frame pipe: a line with "reply" answers the
// request with that id, and every other line is a log record. stdout and
// stderr belong to pipeline code and never carry frames.

import { createInterface } from "node:readline";
import type { Readable } from "node:stream";
import type { LineSink } from "./log.ts";

export const OPS = ["describe", "plan", "run_node", "run_step", "cancel", "eval"] as const;
export type Op = (typeof OPS)[number];

/** Error codes an SDK sends in a failed reply. */
export type ErrorCode =
  | "unknown_op"
  | "unknown_pipeline"
  | "unknown_node"
  | "unknown_step"
  | "unknown_closure"
  | "no_plan"
  | "bad_request"
  | "invalid_args"
  | "plan_failed";

/** The largest step output value, as JSON, the host records: 16 MiB. */
export const MAX_OUTPUT_BYTES = 16 << 20;

/** Every frame, newline included, fits in 17 MiB: an output of MAX_OUTPUT_BYTES and its reply's own fields. */
export const MAX_FRAME_BYTES = MAX_OUTPUT_BYTES + (1 << 20);

export interface Request {
  id: string;
  op: string;
  [field: string]: unknown;
}

export type Reply =
  { reply: string; ok: true; result: unknown } | { reply: string; ok: false; error: { code?: ErrorCode; message: string } };

/** The SDK could not do what a request asked; its code goes in the failed reply. */
export class ProtocolError extends Error {
  readonly code: ErrorCode;

  constructor(code: ErrorCode, message: string) {
    super(message);
    this.code = code;
  }
}

/** A request line the SDK cannot answer, because it names no id to answer. */
export class UnanswerableRequest extends Error {}

/**
 * Yields each request on the request pipe. A line that carries an id but is
 * otherwise malformed is yielded as a ProtocolError to answer; a line with no
 * id throws UnanswerableRequest, since the host would wait on a reply forever.
 */
export async function* readRequests(input: Readable): AsyncGenerator<Request | { id: string; error: ProtocolError }> {
  const lines = createInterface({ input, crlfDelay: Infinity });
  for await (const line of lines) {
    if (line.trim() === "") continue;
    let parsed: unknown;
    try {
      parsed = JSON.parse(line);
    } catch (err) {
      throw new UnanswerableRequest(`request is not JSON: ${(err as Error).message}`);
    }
    const r = parsed as Partial<Request> | null;
    const id = typeof r?.id === "string" && r.id !== "" ? r.id : null;
    if (id === null) throw new UnanswerableRequest("a request needs a non-empty string id");
    if (typeof r?.op !== "string") {
      yield {
        id,
        error: new ProtocolError("bad_request", "a request needs a string op"),
      };
      continue;
    }
    yield r as Request;
  }
}

/**
 * Writes one reply frame. A result too large for one frame becomes a failed
 * reply, so the host never waits on an id the SDK dropped.
 */
export function writeReply(out: LineSink, reply: Reply): void {
  let line = JSON.stringify(reply) + "\n";
  if (Buffer.byteLength(line) > MAX_FRAME_BYTES) {
    line =
      JSON.stringify({
        reply: reply.reply,
        ok: false,
        error: {
          message: `reply exceeds the ${MAX_FRAME_BYTES}-byte frame limit`,
        },
      }) + "\n";
  }
  out.write(line);
}
