import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Agent, ServiceStatus } from "./api";
import {
  fleetHeadroomState,
  fleetLocation,
  fleetRegistration,
  fleetSlotTotals,
  fleetServiceSummary,
  fleetSlots,
  formatFleetResources,
  sortFleetAgents,
} from "./fleet";

function agent(fields: Partial<Agent>): Agent {
  return {
    name: "runner",
    type: "agent",
    labels: {},
    last_seen: "2026-09-02T20:00:00Z",
    status: "idle",
    max_concurrent: 2,
    ...fields,
  };
}

describe("fleet presentation", () => {
  it("summarizes healthy and degraded services", () => {
    const controller: ServiceStatus = {
      name: "controller",
      url: "/health",
      status: "ok",
      latency_ms: 3,
      checked_at: "2026-09-24T00:00:00Z",
    };
    const summary = fleetServiceSummary([controller]);
    assert.equal(summary.overall, "ok");
    assert.deepEqual(summary.serviceProbes, [controller]);

    const degraded = { ...controller, status: "degraded", problems: ["db: unavailable"] };
    const withOutage = fleetServiceSummary([degraded]);
    assert.equal(withOutage.overall, "degraded");
    assert.deepEqual(withOutage.serviceProbes[0].problems, ["db: unavailable"]);
  });
  it("does not infer a missing location", () => {
    assert.equal(fleetLocation(agent({})), "unknown");
  });

  it("separates registered policy from legacy activity", () => {
    assert.equal(fleetRegistration(agent({ active_slots: 0, max_concurrent: 2 })), "registered");
    assert.equal(fleetRegistration(agent({ max_concurrent: 2 })), "legacy");
    assert.equal(fleetRegistration(agent({ max_concurrent: 0 })), "legacy");
  });

  it("distinguishes fresh, stale, and absent headroom observations", () => {
    assert.equal(
      fleetHeadroomState(
        agent({
          headroom: {
            cores: 2,
            memory_bytes: 4 << 30,
            queue_depth: 0,
            observed_at: "2026-09-02T20:00:00Z",
          },
        }),
      ),
      "reported",
    );
    assert.equal(fleetHeadroomState(agent({ status: "offline" })), "stale");
    assert.equal(fleetHeadroomState(agent({ status: "idle" })), "not-reported");
  });

  it("keeps offline executors after active ones", () => {
    const rows = [
      agent({ name: "offline", status: "offline" }),
      agent({ name: "idle", status: "idle" }),
      agent({ name: "busy", status: "busy" }),
    ].sort(sortFleetAgents);
    assert.deepEqual(
      rows.map((row) => row.name),
      ["busy", "idle", "offline"],
    );
  });

  it("distinguishes unknown slots and capacity from zero headroom", () => {
    assert.equal(
      fleetSlots(agent({ active_jobs: ["one", "two"], max_concurrent: 4 })),
      "-/4",
    );
    assert.equal(
      fleetSlots(agent({ active_slots: 2, max_concurrent: 4 })),
      "2/4",
    );
    assert.equal(formatFleetResources(), "unknown");
    assert.equal(
      formatFleetResources({ cores: 0, memory_bytes: 0 }),
      "0 cores / 0.0 GiB",
    );
  });

  it("does not turn distinct run IDs into slot utilization", () => {
    assert.deepEqual(
      fleetSlotTotals([
        agent({ active_jobs: ["one", "two"], max_concurrent: 4 }),
      ]),
      { active: null, capacity: 4 },
    );
    assert.deepEqual(
      fleetSlotTotals([
        agent({ active_slots: 2, max_concurrent: 4 }),
        agent({ name: "gateway", active_slots: 1, max_concurrent: 2 }),
      ]),
      { active: 3, capacity: 6 },
    );
  });
});
