import type { Readable } from "node:stream";
import { StringDecoder } from "node:string_decoder";
import type { LineSink } from "./log.ts";
import { decodeError, failure, object, string, type ErrorFields } from "./errors.ts";

export const OPS = ["hello", "describe", "plan", "run_step", "eval", "cancel"] as const;
export const MAX_OUTPUT_BYTES = 16 << 20;
export const MAX_FRAME_BYTES = MAX_OUTPUT_BYTES + (1 << 20);
export interface Request {
  id: string;
  op: string;
  [field: string]: unknown;
}
export type Reply = { reply: string; ok: true; result: unknown } | { reply: string; ok: false; error: ErrorFields };
export class UnanswerableRequest extends Error {}

export async function* readRequests(input: Readable): AsyncGenerator<Request> {
  const decoder = new StringDecoder("utf8");
  let pending = "";
  for await (const chunk of input) {
    pending += typeof chunk === "string" ? chunk : decoder.write(chunk);
    let end: number;
    while ((end = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, end);
      pending = pending.slice(end + 1);
      if (Buffer.byteLength(line) > MAX_FRAME_BYTES) throw new UnanswerableRequest("request exceeds frame limit");
      if (line.trim()) yield parseRequest(line);
    }
    if (Buffer.byteLength(pending) > MAX_FRAME_BYTES) throw new UnanswerableRequest("request exceeds frame limit");
  }
  pending += decoder.end();
  if (pending.trim()) yield parseRequest(pending);
}
function parseRequest(line: string): Request {
  let value: unknown;
  try {
    value = JSON.parse(line);
  } catch {
    throw new UnanswerableRequest("request is not JSON");
  }
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    typeof (value as Request).id !== "string" ||
    !(value as Request).id
  )
    throw new UnanswerableRequest("request needs a nonempty string id");
  return value as Request;
}
const fields: Record<string, readonly string[]> = {
  hello: ["protocol", "engine", "runner", "binding"],
  describe: [],
  plan: ["pipeline", "args", "run"],
  run_step: ["job", "step", "attempt", "failure", "args"],
  eval: ["closure", "input", "job", "step", "attempt", "failure", "args"],
  cancel: ["job", "step"],
};
const operationFields = new Set(Object.values(fields).flat());
export function validateRequest(req: Request): void {
  if (typeof req.op !== "string") throw failure("bad_request", "op must be a string");
  if (!(OPS as readonly string[]).includes(req.op)) throw failure("unknown_op", `unknown operation ${req.op}`);
  for (const key of Object.keys(req))
    if (operationFields.has(key) && !fields[req.op]!.includes(key)) throw failure("bad_request", `${key} contradicts ${req.op}`);
  if (req.op === "hello") {
    if (!Number.isInteger(req.protocol)) throw failure("bad_request", "protocol must be an integer");
    string(object(req.engine, "engine").version, "engine.version");
    const runner = object(req.runner, "runner");
    string(runner.name, "runner.name");
    string(runner.type, "runner.type");
    if (!Array.isArray(runner.labels) || runner.labels.some((v) => typeof v !== "string"))
      throw failure("bad_request", "runner.labels must be strings");
    if (req.binding !== undefined) {
      const binding = object(req.binding, "binding");
      string(binding.run_id, "binding.run_id");
      string(binding.job_id, "binding.job_id");
      const routes = object(binding.routes, "binding.routes");
      string(routes.url, "binding.routes.url");
      string(routes.bearer, "binding.routes.bearer");
    }
  }
  if (req.op === "plan") {
    string(req.pipeline, "pipeline");
    stringMap(req.args, "args");
    const run = object(req.run, "run");
    string(run.run_id, "run.run_id");
    string(run.pipeline, "run.pipeline");
    for (const key of ["dry_run", "no_memo"])
      if (run[key] !== undefined && typeof run[key] !== "boolean") throw failure("bad_request", `run.${key} must be boolean`);
    if (run.git !== undefined) {
      const git = object(run.git, "run.git");
      for (const key of ["sha", "branch"])
        if (typeof git[key] !== "string") throw failure("bad_request", `run.git.${key} must be a string`);
      optionalStrings(git, ["repo", "repo_url", "default_branch"], "run.git");
    }
    if (run.trigger !== undefined) {
      const trigger = object(run.trigger, "run.trigger");
      string(trigger.source, "run.trigger.source");
      optionalStrings(trigger, ["user"], "run.trigger");
      if (trigger.pull_request !== undefined) {
        const pr = object(trigger.pull_request, "run.trigger.pull_request");
        if (!Number.isSafeInteger(pr.number) || Number(pr.number) < 1)
          throw failure("bad_request", "pull request number must be a positive integer");
        optionalStrings(pr, ["action", "base_ref", "base_sha", "head_ref", "head_sha"], "run.trigger.pull_request");
      }
    }
    if (run.started_at !== undefined) {
      const started = string(run.started_at, "run.started_at");
      if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(started) || !Number.isFinite(Date.parse(started)))
        throw failure("bad_request", "run.started_at must be RFC3339");
    }
  }
  if (req.op === "run_step" || req.op === "cancel") string(req.job, "job");
  if (req.op === "run_step" || req.step !== undefined) string(req.step, "step");
  if (req.job !== undefined) string(req.job, "job");
  if (req.op === "run_step" || req.attempt !== undefined) {
    if (!Number.isSafeInteger(req.attempt) || Number(req.attempt) < 1) throw failure("bad_request", "attempt must be a positive integer");
  }
  if (req.op === "eval") string(req.closure, "closure");
  if (req.args !== undefined) stringMap(req.args, "args");
  if (req.failure !== undefined) {
    const f = object(req.failure, "failure");
    string(f.job, "failure.job");
    string(f.stage, "failure.stage");
    decodeError(f.error);
  }
}
function optionalStrings(value: Record<string, unknown>, keys: readonly string[], label: string): void {
  for (const key of keys)
    if (value[key] !== undefined && typeof value[key] !== "string") throw failure("bad_request", `${label}.${key} must be a string`);
}
export function stringMap(value: unknown, label: string): Record<string, string> {
  const values = object(value, label);
  for (const value of Object.values(values))
    if (typeof value !== "string") throw failure("bad_request", `${label} must contain only strings`);
  return values as Record<string, string>;
}
export function jsonOutput(value: unknown): unknown {
  if (value === undefined) return undefined;
  const json = JSON.stringify(value, (_key, child: unknown) => {
    if (
      typeof child === "function" ||
      typeof child === "symbol" ||
      typeof child === "bigint" ||
      (typeof child === "number" && !Number.isFinite(child))
    )
      throw failure("sdk_error", "output must be JSON serializable");
    return child;
  });
  if (json === undefined) throw failure("sdk_error", "output must be JSON serializable");
  if (Buffer.byteLength(json) > MAX_OUTPUT_BYTES) throw failure("sdk_error", `output exceeds ${MAX_OUTPUT_BYTES} bytes`);
  return JSON.parse(json);
}
export function writeReply(out: LineSink, reply: Reply): void {
  let line = JSON.stringify(reply) + "\n";
  if (Buffer.byteLength(line) > MAX_FRAME_BYTES)
    line = JSON.stringify({ reply: reply.reply, ok: false, error: { code: "sdk_error", message: "reply exceeds frame limit" } }) + "\n";
  out.write(line);
}
