import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Node } from "./api";
import {
  compactExecutionDisplay,
  executionAttemptOrdinal,
  executionAttempts,
  executionAttemptsNewestFirst,
  executionDisplay,
  placementLabel,
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
  it("derives placement from a known historical execution site", () => {
    const display = executionDisplay({ execution_site: "cluster", execution_site_name: "job-a" });
    assert.equal(display.locationLabel, "Cloud");
    assert.equal(display.executorLabel, "cluster job-a");
  });
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
      executor_kind: "agent",
      executor_name: "runner-1",
      execution_site: "machine",
      execution_site_name: "moonborn",
    }).executorLabel, "agent runner-1");
  });
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

  it("shows nothing for a claimed node with no attempt", () => {
    assert.equal(compactExecutionDisplay(node({ claimed: true })), null);
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

describe("placementLabel", () => {
  it("names the runner a preference chose", () => {
    assert.equal(
      placementLabel(
        node({ claimed_by: "runner:laptop:1", placement_reason: "preference" }),
      ),
      "runner:laptop:1 (preferred)",
    );
  });

  it("says a fallback followed the hold", () => {
    assert.equal(
      placementLabel(
        node({ claimed_by: "runner:cloudpod:1", placement_reason: "fallback" }),
      ),
      "runner:cloudpod:1 (fallback after the local-first hold)",
    );
  });

  it("says nothing about a node nobody preferred", () => {
    assert.equal(
      placementLabel(
        node({ claimed_by: "runner:cloudpod:1", placement_reason: "none" }),
      ),
      null,
    );
  });

  it("says nothing about an unclaimed node", () => {
    assert.equal(placementLabel(node({ placement_reason: "preference" })), null);
  });
});
