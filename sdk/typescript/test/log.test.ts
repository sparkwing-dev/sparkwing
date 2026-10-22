import assert from "node:assert/strict";
import { test } from "node:test";
import { LogWriter } from "../src/log.ts";
import { MAX_FRAME_BYTES } from "../src/protocol.ts";

test("logs retain debug and structured attributes without assigning host job identity", () => {
  const records: any[] = [];
  const log = new LogWriter({ write: (line) => records.push(JSON.parse(line)) }, "compile", () => new Date("2026-01-01T00:00:00Z"));
  log.debug("details", { value: 1 });
  log.event("future_event", { bytes: 7 });
  assert.deepEqual(records, [
    { ts: "2026-01-01T00:00:00.000Z", level: "debug", step: "compile", msg: "details", attrs: { value: 1 } },
    { ts: "2026-01-01T00:00:00.000Z", level: "info", step: "compile", event: "future_event", attrs: { bytes: 7 } },
  ]);
});

test("logs never write an encoded frame above the protocol limit", () => {
  const frames: string[] = [];
  const log = new LogWriter({ write: (line) => frames.push(line) });
  assert.throws(() => log.info("x".repeat(MAX_FRAME_BYTES)), /frame limit/);
  assert.equal(frames.length, 0);
  for (const value of ["é", "秘密", '"', "\\", "\n", "x"]) {
    try {
      log.event("exec_line", { line: value.repeat(Math.floor(MAX_FRAME_BYTES / 6)), nested: { value } });
    } catch (error) {
      assert.match(String(error), /frame limit/);
    }
  }
  assert.ok(frames.length > 0);
  assert.ok(frames.every((frame) => Buffer.byteLength(frame) <= MAX_FRAME_BYTES));
});

test("command chunks remain base64 transport until the host masks and splits them", () => {
  const records: any[] = [];
  const log = new LogWriter({ write: (line) => records.push(JSON.parse(line)) }, "compile");
  const data = Buffer.from("before\nafter").toString("base64");
  log.event("exec_chunk", { stream: "command-stdout", data });
  log.event("exec_chunk", { stream: "command-stdout", end: true });
  assert.deepEqual(records.map(({event,step,stream,stream_data,stream_end,msg}) => ({event,step,stream,stream_data,stream_end,msg})), [
    { event: "exec_line", step: "compile", stream: "command-stdout", stream_data: data, stream_end: undefined, msg: undefined },
    { event: "exec_line", step: "compile", stream: "command-stdout", stream_data: undefined, stream_end: true, msg: undefined },
  ]);
});
