import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { CapacityProfile, CapacityRankSelection } from "./api.ts";
import {
  chargeBasisLabel,
  mismatchNote,
  pinDriftFlag,
  rankLabel,
  sortProfiles,
} from "./capacity.ts";

function profile(over: Partial<CapacityProfile>): CapacityProfile {
  return {
    pipeline: "demo",
    charge: {
      cores: 1,
      memory_bytes: 0,
      source: "measured",
      cores_basis: "sustained_p95",
    },
    sample_count: 0,
    peak_cores: 0,
    sustained_cores: 0,
    peak_memory_bytes: 0,
    cpu_p50: 0,
    cpu_p95: 0,
    memory_p50_bytes: 0,
    memory_p95_bytes: 0,
    cpu_measured: true,
    p50_duration_ms: 0,
    p99_duration_ms: 0,
    ...over,
  };
}

function selection(
  over: Partial<CapacityRankSelection>,
): CapacityRankSelection {
  return {
    field: "sustained_cores",
    percentile: 0.95,
    rank: 10,
    count: 10,
    index: 9,
    value: 4,
    stored: 4,
    matches: true,
    ...over,
  };
}

describe("sortProfiles", () => {
  const rows = [
    profile({
      pipeline: "b",
      charge: {
        cores: 2,
        memory_bytes: 5,
        source: "measured",
        cores_basis: "sustained_p95",
      },
    }),
    profile({
      pipeline: "a",
      charge: {
        cores: 8,
        memory_bytes: 1,
        source: "measured",
        cores_basis: "sustained_p95",
      },
    }),
    profile({
      pipeline: "c",
      charge: {
        cores: 2,
        memory_bytes: 9,
        source: "measured",
        cores_basis: "sustained_p95",
      },
    }),
  ];

  it("orders by charge, heaviest first", () => {
    assert.deepEqual(
      sortProfiles(rows, "charge", false).map((p) => p.pipeline),
      ["a", "b", "c"],
    );
  });

  it("breaks ties by name so equal rows hold still", () => {
    assert.deepEqual(
      sortProfiles(rows, "charge", true).map((p) => p.pipeline),
      ["b", "c", "a"],
    );
  });

  it("sorts by name without consulting the numbers", () => {
    assert.deepEqual(
      sortProfiles(rows, "pipeline", true).map((p) => p.pipeline),
      ["a", "b", "c"],
    );
  });

  it("leaves the input array untouched", () => {
    sortProfiles(rows, "charge", false);
    assert.equal(rows[0].pipeline, "b");
  });
});

describe("rank reporting", () => {
  it("states a selection as rank arithmetic", () => {
    assert.equal(rankLabel(selection({})), "p95 = rank 10 of 10");
    assert.equal(
      rankLabel(selection({ count: 0, unmeasured: true })),
      "no samples",
    );
  });

  it("stays quiet when the window reproduces the stored charge", () => {
    assert.equal(mismatchNote(selection({}), "sustained cores"), "");
  });

  it("calls out a charge its own window does not support", () => {
    const note = mismatchNote(
      selection({ matches: false, stored: 9, value: 4 }),
      "sustained cores",
    );
    assert.match(note, /Stored sustained cores is 9/);
    assert.match(note, /disagree/);
  });
});

describe("labels", () => {
  it("spells out the basis a charge came from", () => {
    assert.equal(chargeBasisLabel("sustained_p95"), "sustained p95");
    assert.equal(chargeBasisLabel("unknown_basis"), "unknown_basis");
  });

  it("flags a pin only when one is set, and says which way it drifted", () => {
    assert.equal(pinDriftFlag(profile({})), "");
    assert.equal(pinDriftFlag(profile({ pinned_cores: 4 })), "pinned");
    assert.equal(
      pinDriftFlag(profile({ pinned_cores: 4, drift_class: "over_pinned" })),
      "pin high",
    );
    assert.equal(
      pinDriftFlag(
        profile({ pinned_memory_bytes: 1024, drift_class: "under_pinned" }),
      ),
      "pin low",
    );
  });
});
