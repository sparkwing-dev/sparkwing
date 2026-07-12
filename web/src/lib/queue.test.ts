import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { QueueHolder, QueueState } from "./api.ts";
import {
  daemonUptimeLabel,
  eventsLine,
  externalPressureNote,
  fmtCost,
  fmtDuration,
  fmtETA,
  fmtHolderCost,
  groupHolders,
  hasDaemon,
  humanBytes,
  resourceAvailable,
  trimFloat,
} from "./queue.ts";

describe("trimFloat", () => {
  it("renders whole numbers bare and fractions to two places", () => {
    assert.equal(trimFloat(4), "4");
    assert.equal(trimFloat(4.5), "4.50");
  });
});

describe("humanBytes", () => {
  it("scales bytes to the largest unit", () => {
    assert.equal(humanBytes(512), "512 B");
    assert.equal(humanBytes(2 * 1024 * 1024), "2.0 MiB");
    assert.equal(humanBytes(3 * 1024 * 1024 * 1024), "3.0 GiB");
  });
});

describe("fmtCost", () => {
  it("renders cores, adding memory only when charged", () => {
    assert.equal(fmtCost({ cores: 2 }), "2 cores");
    assert.equal(
      fmtCost({ cores: 1.5, memory_bytes: 1024 * 1024 * 1024 }),
      "1.50 cores, 1.0 GiB",
    );
  });
  it("treats a missing charge as zero cores", () => {
    assert.equal(fmtCost(undefined), "0 cores");
  });
});

describe("fmtHolderCost", () => {
  it("dashes an attached child that rides its parent lease", () => {
    const child: QueueHolder = {
      run_id: "c",
      parent: "p",
      elapsed_ms: 0,
      resources: { cores: 2 },
    };
    assert.equal(fmtHolderCost(child), "-");
  });
  it("charges a top-level holder its own cost", () => {
    const h: QueueHolder = {
      run_id: "p",
      elapsed_ms: 0,
      resources: { cores: 2 },
    };
    assert.equal(fmtHolderCost(h), "2 cores");
  });
});

describe("fmtDuration", () => {
  it("rounds to whole seconds and collapses to compact units", () => {
    assert.equal(fmtDuration(0), "-");
    assert.equal(fmtDuration(3200), "3s");
    assert.equal(fmtDuration(90_000), "1m 30s");
    assert.equal(fmtDuration(120_000), "2m");
    assert.equal(fmtDuration(3_600_000), "1h");
    assert.equal(fmtDuration(3_900_000), "1h 5m");
  });
});

describe("fmtETA", () => {
  it("shows now, a span, or a dash when no estimate exists", () => {
    assert.equal(fmtETA(null), "-");
    assert.equal(fmtETA(undefined), "-");
    assert.equal(fmtETA(0), "now");
    assert.equal(fmtETA(5000), "5s");
  });
});

describe("resourceAvailable", () => {
  it("uses headroom-aware available for host dimensions", () => {
    assert.equal(
      resourceAvailable({
        key: "cores",
        capacity: 8,
        held: 2,
        reserved: 1,
        external: 3,
        available: 2,
      }),
      2,
    );
  });
  it("falls back to capacity-minus-held for semaphores and old daemons", () => {
    assert.equal(resourceAvailable({ key: "deploy", capacity: 3, held: 1 }), 2);
    assert.equal(resourceAvailable({ key: "cores", capacity: 4, held: 1 }), 3);
  });
  it("floors a negative remainder at zero", () => {
    assert.equal(resourceAvailable({ key: "deploy", capacity: 1, held: 3 }), 0);
  });
});

describe("externalPressureNote", () => {
  it("fires only when external load is the binding constraint on a waiter", () => {
    const qs: QueueState = {
      resources: [{ key: "cores", capacity: 8, held: 2, external: 5 }],
      waiters: [
        {
          run_id: "w",
          position: 1,
          resources: { cores: 4 },
          blocking_reason: "needs 4.0 cores; 1.0 available (external load 5.0)",
        },
      ],
    };
    assert.match(externalPressureNote(qs), /External .* binding constraint/);
  });
  it("stays quiet with no waiters", () => {
    const qs: QueueState = {
      resources: [{ key: "cores", capacity: 8, held: 2, external: 5 }],
    };
    assert.equal(externalPressureNote(qs), "");
  });
  it("stays quiet when the wait is pure arrival order, not external load", () => {
    const qs: QueueState = {
      resources: [{ key: "cores", capacity: 8, held: 2, external: 0 }],
      waiters: [{ run_id: "w", position: 1, resources: { cores: 4 } }],
    };
    assert.equal(externalPressureNote(qs), "");
  });
});

describe("groupHolders", () => {
  it("nests attached children under their parent", () => {
    const holders: QueueHolder[] = [
      { run_id: "p", elapsed_ms: 0, resources: { cores: 2 } },
      { run_id: "c1", parent: "p", elapsed_ms: 0, resources: {} },
      { run_id: "c2", parent: "p", elapsed_ms: 0, resources: {} },
      { run_id: "q", elapsed_ms: 0, resources: { cores: 1 } },
    ];
    const groups = groupHolders(holders);
    assert.equal(groups.length, 2);
    assert.equal(groups[0].holder.run_id, "p");
    assert.deepEqual(
      groups[0].children.map((c) => c.run_id),
      ["c1", "c2"],
    );
    assert.equal(groups[1].holder.run_id, "q");
  });
  it("promotes an orphaned child whose parent is absent", () => {
    const holders: QueueHolder[] = [
      { run_id: "c", parent: "gone", elapsed_ms: 0, resources: { cores: 1 } },
    ];
    const groups = groupHolders(holders);
    assert.equal(groups.length, 1);
    assert.equal(groups[0].holder.run_id, "c");
    assert.equal(groups[0].children.length, 0);
  });
});

describe("eventsLine", () => {
  it("summarizes grants with median wait and only the troubles that occurred", () => {
    const line = eventsLine({
      window_ms: 24 * 3_600_000,
      runs: 12,
      median_wait_ms: 3000,
      evictions: [{ key: "deploy", count: 2 }],
      cancellations: 1,
    });
    assert.equal(
      line,
      "last 24h: 12 runs, median wait 3s, 2 evictions (key: deploy), 1 cancellation",
    );
  });
  it("shows a zero median as 0s, not a dash, when runs occurred", () => {
    const line = eventsLine({
      window_ms: 24 * 3_600_000,
      runs: 3,
      median_wait_ms: 0,
    });
    assert.equal(line, "last 24h: 3 runs, median wait 0s");
  });
  it("is empty for a quiet or absent window", () => {
    assert.equal(eventsLine(null), "");
    assert.equal(
      eventsLine({ window_ms: 3_600_000, runs: 0, median_wait_ms: 0 }),
      "",
    );
  });
});

describe("daemonUptimeLabel", () => {
  it("names just-started and a rounded uptime", () => {
    assert.equal(daemonUptimeLabel({ daemon_uptime_ms: 500 }), "just started");
    assert.equal(daemonUptimeLabel({ daemon_uptime_ms: 90_000 }), "up 1m 30s");
    assert.equal(daemonUptimeLabel({}), "");
  });
});

describe("hasDaemon", () => {
  it("is false for the empty no-daemon payload", () => {
    assert.equal(hasDaemon({}), false);
    assert.equal(hasDaemon({ holders: [], waiters: [], resources: [] }), false);
  });
  it("is true once a version, uptime, or any row is present", () => {
    assert.equal(hasDaemon({ daemon_version: "v0.16.0" }), true);
    assert.equal(hasDaemon({ daemon_uptime_ms: 1000 }), true);
    assert.equal(
      hasDaemon({ resources: [{ key: "cores", capacity: 8, held: 0 }] }),
      true,
    );
  });
});
