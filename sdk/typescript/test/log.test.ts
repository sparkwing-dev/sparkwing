import assert from "node:assert/strict";
import { test } from "node:test";
import { LogWriter } from "../src/log.ts";

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
