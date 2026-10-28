import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Node } from "./api";
import {
  compactExecutionDisplay,
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
  it("has nothing to attribute before an attempt starts", () => {
    assert.deepEqual(executionAttempts(node({})), []);
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
  it("maps known execution origins to a compact icon and specific tooltip", () => {
    assert.deepEqual(
      [
        executionDisplay({ executor_kind: "agent", executor_name: "moonborn", location: "local" }),
        executionDisplay({ executor_kind: "github-actions", executor_name: "koreyGambill/moonborn-ws", location: "unknown" }),
        executionDisplay({ executor_kind: "cloud", location: "cloud" }),
        executionDisplay({ executor_kind: "kubernetes", executor_name: "warm-pool", location: "cloud" }),
      ].map(({ icon, tooltip }) => [icon, tooltip]),
      [
        ["machine", "Ran on moonborn (your machine)"],
        ["github", "Ran on GitHub Actions: koreyGambill/moonborn-ws"],
        ["cloud", "Ran in Sparkwing Cloud"],
        ["cluster", "Ran on the cluster warm pool"],
      ],
    );
    assert.equal(executionDisplay({ location: "unknown" }).icon, null);
    assert.equal(executionDisplay({
      executor_kind: "runner",
      executor_name: "runner-1",
    }).executorLabel, "runner runner-1");
  });
  it("uses text and a separate style for every location", () => {
    const local = executionDisplay({
      location: "local",
      executor_name: "box",
    });
    const cloud = executionDisplay({
      location: "cloud",
      executor_name: "pool",
    });
    const unknown = executionDisplay({ executor_name: "legacy" });

    assert.equal(local.locationLabel, "Local");
    assert.equal(cloud.locationLabel, "Cloud");
    assert.equal(unknown.locationLabel, "Location unknown");
    assert.notEqual(local.className, cloud.className);
    assert.notEqual(cloud.className, unknown.className);
  });
});

describe("compactExecutionDisplay", () => {
  it("shows nothing for an attempt whose location is unknown", () => {
    assert.equal(
      compactExecutionDisplay(
        node({
          execution_attempts: [
            { run_id: "run-one", node_id: "build", attempt: 1, location: "unknown" },
          ],
        }),
      ),
      null,
    );
  });

  it("shows nothing for a node with no attempt", () => {
    assert.equal(compactExecutionDisplay(node({})), null);
  });

  it("shows the latest known location", () => {
    const display = compactExecutionDisplay(
      node({
        execution_attempts: [
          { run_id: "run-one", node_id: "build", attempt: 1, location: "cloud" },
          { run_id: "run-one", node_id: "build", attempt: 2, location: "local" },
        ],
      }),
    );
    assert.equal(display?.location, "local");
  });
});
