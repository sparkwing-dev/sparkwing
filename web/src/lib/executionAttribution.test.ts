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
  it("does not turn a claim flag into execution attribution", () => {
    assert.deepEqual(executionAttempts(node({ claimed: true })), []);
  });

  it("adapts the explicit single-attempt fields without guessing location", () => {
    const attempts = executionAttempts(
      node({
        run_id: "run-one",
        executor_kind: "agent",
        executor_name: "desktop",
        execution_started_at: "2026-09-02T20:00:00Z",
      }),
    );
    assert.equal(attempts.length, 1);
    assert.equal(attempts[0].location, "unknown");
    assert.equal(attempts[0].executor_name, "desktop");
    assert.equal(attempts[0].run_id, "run-one");
    assert.equal(attempts[0].node_id, "build");
  });

  it("sorts durable attempts by numeric ordinal", () => {
    const attempts = [
      {
        run_id: "run-one",
        node_id: "build",
        attempt: 2,
        executor_kind: "kubernetes",
        executor_name: "pool-a",
        location: "cloud" as const,
        outcome: "success",
      },
      {
        run_id: "run-one",
        node_id: "build",
        attempt: 1,
        executor_kind: "agent",
        executor_name: "desktop",
        location: "local" as const,
        outcome: "agent_lost",
        retry_run_id: "run-two",
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
      executor_name: "malformed",
    } as unknown as import("./api").ExecutionAttempt;
    const attempts = [
      { attempt: 2, executor_name: "two-a" },
      malformed,
      { attempt: 1, executor_name: "one" },
      { attempt: 2, executor_name: "two-b" },
      { executor_name: "missing" },
    ];
    const n = node({ execution_attempts: attempts });
    assert.deepEqual(
      executionAttempts(n).map((attempt) => attempt.executor_name),
      ["malformed", "missing", "one", "two-a", "two-b"],
    );
    assert.deepEqual(
      executionAttemptsNewestFirst(n).map((attempt) => attempt.executor_name),
      ["two-b", "two-a", "one", "missing", "malformed"],
    );
    assert.equal(executionAttemptOrdinal(malformed), null);
    assert.equal(executionAttemptOrdinal({ attempt: 0 }), null);
  });
});

describe("executionDisplay", () => {
  it("uses text and a separate style for every location", () => {
    const local = executionDisplay({
      location: "local",
      executor_name: "box",
      platform: "darwin/arm64",
    });
    const cloud = executionDisplay({
      location: "cloud",
      executor_name: "pool",
    });
    const unknown = executionDisplay({ executor_name: "legacy" });

    assert.equal(local.locationLabel, "Local");
    assert.equal(local.platformLabel, "darwin/arm64");
    assert.equal(cloud.locationLabel, "Cloud");
    assert.equal(cloud.platformLabel, null);
    assert.equal(unknown.locationLabel, "Location unknown");
    assert.notEqual(local.className, cloud.className);
    assert.notEqual(cloud.className, unknown.className);
  });
});
