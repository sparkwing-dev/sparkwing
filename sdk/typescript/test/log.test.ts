import assert from "node:assert/strict";
import { test } from "node:test";
import { LogWriter } from "../src/log.ts";
import { Lines } from "./helpers.ts";

const fixed = () => new Date("2026-01-02T03:04:05.000Z");

test("records are one JSON object per line, scoped to node and step", () => {
  const out = new Lines();
  const log = new LogWriter(out, { node: "build" }, fixed);
  log.info("hello", { n: 1 });
  log.forStep("compile").warn("careful");
  log.event("exec_end", { cpu_ms: 12, max_rss: 4096 });

  assert.deepEqual(out.json(), [
    { ts: "2026-01-02T03:04:05.000Z", level: "info", msg: "hello", attrs: { n: 1 }, node: "build" },
    { ts: "2026-01-02T03:04:05.000Z", level: "warn", msg: "careful", node: "build", step: "compile" },
    { ts: "2026-01-02T03:04:05.000Z", level: "info", event: "exec_end", attrs: { cpu_ms: 12, max_rss: 4096 }, node: "build" },
  ]);
});

test("a message with newlines stays on one line", () => {
  const out = new Lines();
  new LogWriter(out, {}, fixed).info("a\nb");
  assert.equal(out.lines.length, 1);
  assert.equal(out.json()[0]?.["msg"], "a\nb");
});
