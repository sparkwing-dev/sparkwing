import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it } from "node:test";
import { listenForRunEvents } from "./useRunEvents";

describe("listenForRunEvents", () => {
  it("hears every registered state and control event kind", () => {
    const produced: string[] = JSON.parse(readFileSync(join(__dirname, "runEventKinds.json"), "utf8"));
    const heard: string[] = [];
    listenForRunEvents({ addEventListener: (kind) => heard.push(kind) }, () => {});
    assert.deepEqual(heard, produced);
    for (const kind of ["run_requeued", "debug.paused", "debug.resumed", "secret_preflight_refused", "job_started"]) {
      assert.ok(heard.includes(kind), kind);
    }
  });
});
