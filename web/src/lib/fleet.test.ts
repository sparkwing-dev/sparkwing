import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Runner } from "./api";
import {
  fleetHeadroomState,
  fleetLocation,
  formatFleetResources,
  sortFleetRunners,
} from "./fleet";

function agent(fields: Partial<Runner>): Runner {
  return {
    name: "runner",
    class: "runner",
    location: "",
    labels: [],
    reserved_pools: [],
    repos: [],
    last_seen: "2026-09-02T20:00:00Z",
    status: "idle",
    active_jobs: [],
    max_concurrent: 2,
    metered: false,
    capacity: { cores: 0, memory_bytes: 0 },
    ...fields,
  };
}

describe("fleet presentation", () => {
  it("does not infer a missing location", () => {
    assert.equal(fleetLocation(agent({})), "unknown");
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
    ].sort(sortFleetRunners);
    assert.deepEqual(
      rows.map((row) => row.name),
      ["busy", "idle", "offline"],
    );
  });

  it("distinguishes unknown headroom from zero headroom", () => {
    assert.equal(formatFleetResources(), "unknown");
    assert.equal(
      formatFleetResources({ cores: 0, memory_bytes: 0 }),
      "0 cores / 0.0 GiB",
    );
  });
});
