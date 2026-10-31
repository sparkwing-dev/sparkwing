import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Run } from "./api";
import { hasTimestamp, runElapsedMs, runStartedAt, runTimestamp } from "./runTime";

const created = "2026-10-30T10:00:00Z";
const started = "2026-10-30T10:01:00Z";
const finished = "2026-10-30T10:02:00Z";

function run(overrides: Partial<Run> = {}): Run {
  return { id: "queued", pipeline: "build", status: "pending", started_at: "0001-01-01T00:00:00Z", created_at: created, ...overrides };
}

describe("run timing", () => {
  it("treats missing, invalid and Go zero times as absent", () => {
    for (const value of [undefined, "", "bad", "0001-01-01T00:00:00Z"]) assert.equal(hasTimestamp(value), false);
    assert.equal(hasTimestamp(created), true);
  });

  it("uses creation time for queued and cancelled-before-start runs without a duration", () => {
    for (const status of ["pending", "queued", "cancelled"]) {
      const queued = run({ status, finished_at: status === "cancelled" ? finished : undefined });
      assert.equal(runStartedAt(queued), undefined);
      assert.equal(runTimestamp(queued), created);
      assert.equal(runElapsedMs(queued), undefined);
    }
    assert.equal(runTimestamp(run({ created_at: undefined })), undefined);
  });

  it("measures started runs from their start and ignores zero finishes", () => {
    assert.equal(runTimestamp(run({ started_at: started })), started);
    assert.equal(runElapsedMs(run({ started_at: started, finished_at: finished })), 60_000);
    assert.equal(runElapsedMs(run({ started_at: started, finished_at: "0001-01-01T00:00:00Z" }), Date.parse(finished)), 60_000);
  });
});
