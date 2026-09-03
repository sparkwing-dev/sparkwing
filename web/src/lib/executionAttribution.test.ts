import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Node } from "./api";
import {
  executionAttemptOrdinal,
  executionAttempts,
  executionAttemptsNewestFirst,
  executionDisplay,
} from "./executionAttribution";

function node(fields: Partial<Node>): Node {
  return {
    id: "build",
    status: "done",
    outcome: "success",
    deps: [],
    duration_ms: 100,
    ...fields,
  };
}

describe("executionAttempts", () => {
  it("does not turn legacy claim metadata into local attribution", () => {
    assert.deepEqual(
      executionAttempts(node({ claimed_by: "runner:desktop:1" })),
      [],
    );
  });

  it("adapts the explicit single-attempt fields without guessing location", () => {
    const attempts = executionAttempts(
      node({
        coordinator_id: "personal",
        executor_kind: "agent",
        executor_id: "desktop",
        execution_started_at: "2026-09-02T20:00:00Z",
      }),
    );
    assert.equal(attempts.length, 1);
    assert.equal(attempts[0].location, "unknown");
    assert.equal(attempts[0].executor_id, "desktop");
  });

  it("sorts durable attempts by numeric ordinal", () => {
    const attempts = [
      {
        attempt: 2,
        executor_kind: "kubernetes",
        executor_id: "pool-a",
        location: "cloud" as const,
        outcome: "success",
      },
      {
        attempt: 1,
        executor_kind: "agent",
        executor_id: "desktop",
        location: "local" as const,
        outcome: "agent_lost",
      },
    ];
    const n = node({ execution_attempts: attempts });
    assert.deepEqual(
      executionAttempts(n).map((attempt) => attempt.attempt),
      [1, 2],
    );
    assert.deepEqual(
      executionAttemptsNewestFirst(n).map((attempt) => attempt.attempt),
      [2, 1],
    );
  });

  it("keeps duplicate ordinals stable and malformed ordinals unsequenced", () => {
    const malformed = {
      attempt: "third",
      executor_id: "malformed",
    } as unknown as import("./api").ExecutionAttempt;
    const attempts = [
      { attempt: 2, executor_id: "two-a" },
      malformed,
      { attempt: 1, executor_id: "one" },
      { attempt: 2, executor_id: "two-b" },
      { executor_id: "missing" },
    ];
    const n = node({ execution_attempts: attempts });
    assert.deepEqual(
      executionAttempts(n).map((attempt) => attempt.executor_id),
      ["malformed", "missing", "one", "two-a", "two-b"],
    );
    assert.deepEqual(
      executionAttemptsNewestFirst(n).map((attempt) => attempt.executor_id),
      ["two-b", "two-a", "one", "missing", "malformed"],
    );
    assert.equal(executionAttemptOrdinal(malformed), null);
    assert.equal(executionAttemptOrdinal({ attempt: 0 }), null);
  });
});

describe("executionDisplay", () => {
  it("uses text and a separate style for every location", () => {
    const local = executionDisplay({ location: "local", executor_id: "box" });
    const cloud = executionDisplay({ location: "cloud", executor_id: "pool" });
    const unknown = executionDisplay({ executor_id: "legacy" });

    assert.equal(local.locationLabel, "Local");
    assert.equal(cloud.locationLabel, "Cloud");
    assert.equal(unknown.locationLabel, "Location unknown");
    assert.notEqual(local.className, cloud.className);
    assert.notEqual(cloud.className, unknown.className);
  });
});
