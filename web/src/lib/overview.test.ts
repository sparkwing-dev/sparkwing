import { describe, it } from "node:test";
import assert from "node:assert/strict";
import type { Run } from "./api.ts";
import {
  DAY_MS,
  WEEK_MS,
  completedIn,
  deployCount,
  successRate,
  medianBuildMs,
  lastDeploy,
  deltaAbs,
  deltaPct,
  summarize,
} from "./overview.ts";

const NOW = new Date("2026-06-16T12:00:00Z").getTime();

function run(over: Partial<Run> & { id: string }): Run {
  return {
    pipeline: "deploy",
    status: "success",
    started_at: new Date(NOW).toISOString(),
    ...over,
  } as Run;
}

// finishedAgo builds a completed run that finished `ms` before NOW, with
// a build duration of `durMs`.
function finishedAgo(
  id: string,
  ms: number,
  status: string,
  durMs = 60_000,
): Run {
  const finished = NOW - ms;
  return run({
    id,
    status,
    started_at: new Date(finished - durMs).toISOString(),
    finished_at: new Date(finished).toISOString(),
  });
}

describe("completedIn", () => {
  it("excludes running runs without a finished_at", () => {
    const runs = [
      run({ id: "a", status: "running", finished_at: undefined }),
      finishedAgo("b", DAY_MS, "success"),
    ];
    const got = completedIn(runs, NOW - WEEK_MS, NOW);
    assert.deepEqual(
      got.map((r) => r.id),
      ["b"],
    );
  });

  it("is half-open: includes start, excludes end", () => {
    const runs = [finishedAgo("edge", 0, "success")];
    assert.equal(completedIn(runs, NOW - DAY_MS, NOW).length, 0);
    assert.equal(completedIn(runs, NOW - DAY_MS, NOW + 1).length, 1);
  });
});

describe("deployCount", () => {
  it("counts completed runs in the window regardless of outcome", () => {
    const runs = [
      finishedAgo("a", DAY_MS, "success"),
      finishedAgo("b", 2 * DAY_MS, "failed"),
      finishedAgo("c", 2 * WEEK_MS, "success"),
    ];
    assert.equal(deployCount(runs, NOW - WEEK_MS, NOW), 2);
  });
});

describe("successRate", () => {
  it("is success over success+failed, ignoring cancelled", () => {
    const runs = [
      finishedAgo("a", DAY_MS, "success"),
      finishedAgo("b", DAY_MS, "success"),
      finishedAgo("c", DAY_MS, "failed"),
      finishedAgo("d", DAY_MS, "cancelled"),
    ];
    assert.equal(successRate(runs, NOW - WEEK_MS, NOW), 2 / 3);
  });

  it("returns null when nothing resolved to pass or fail", () => {
    const runs = [finishedAgo("a", DAY_MS, "cancelled")];
    assert.equal(successRate(runs, NOW - WEEK_MS, NOW), null);
  });
});

describe("medianBuildMs", () => {
  it("takes the median of successful run durations", () => {
    const runs = [
      finishedAgo("a", DAY_MS, "success", 10_000),
      finishedAgo("b", DAY_MS, "success", 30_000),
      finishedAgo("c", DAY_MS, "success", 20_000),
    ];
    assert.equal(medianBuildMs(runs, NOW - WEEK_MS, NOW), 20_000);
  });

  it("averages the two middle values for an even count", () => {
    const runs = [
      finishedAgo("a", DAY_MS, "success", 10_000),
      finishedAgo("b", DAY_MS, "success", 20_000),
    ];
    assert.equal(medianBuildMs(runs, NOW - WEEK_MS, NOW), 15_000);
  });

  it("ignores failed runs and returns null with no successes", () => {
    const runs = [finishedAgo("a", DAY_MS, "failed", 10_000)];
    assert.equal(medianBuildMs(runs, NOW - WEEK_MS, NOW), null);
  });
});

describe("lastDeploy", () => {
  it("returns the most recently finished run", () => {
    const runs = [
      finishedAgo("old", 3 * DAY_MS, "success"),
      finishedAgo("new", DAY_MS, "failed"),
      run({ id: "running", status: "running", finished_at: undefined }),
    ];
    assert.equal(lastDeploy(runs)?.id, "new");
  });

  it("returns null when nothing has finished", () => {
    const runs = [run({ id: "r", status: "running", finished_at: undefined })];
    assert.equal(lastDeploy(runs), null);
  });
});

describe("summarize", () => {
  it("compares the current window against the anchor-shifted window", () => {
    const runs = [
      // current 7d: two deploys, one in the last 24h
      finishedAgo("c1", 2 * DAY_MS, "success"),
      finishedAgo("c2", 1 * 60_000, "success"),
      // previous week (anchor = 1w): one deploy
      finishedAgo("p1", WEEK_MS + 2 * DAY_MS, "failed"),
    ];
    const ov = summarize(runs, NOW, WEEK_MS);
    assert.equal(ov.deploys7d.current, 2);
    assert.equal(ov.deploys7d.previous, 1);
    assert.equal(ov.deploys1d.current, 1);
    assert.equal(deltaAbs(ov.deploys7d), 1);
    assert.equal(ov.lastDeploy?.id, "c2");
  });

  it("marks build time as lower-is-better and rates higher-is-better", () => {
    const ov = summarize([], NOW, WEEK_MS);
    assert.equal(ov.buildTime.higherIsBetter, false);
    assert.equal(ov.successRate.higherIsBetter, true);
    assert.equal(ov.deploys7d.higherIsBetter, true);
  });
});

describe("deltaPct", () => {
  it("is the fractional change vs previous", () => {
    const m = {
      key: "k",
      label: "l",
      unit: "count" as const,
      current: 12,
      previous: 10,
      higherIsBetter: true,
    };
    assert.equal(deltaPct(m), 0.2);
  });

  it("is null when previous is zero or undefined", () => {
    const base = {
      key: "k",
      label: "l",
      unit: "count" as const,
      higherIsBetter: true,
    };
    assert.equal(deltaPct({ ...base, current: 5, previous: 0 }), null);
    assert.equal(deltaPct({ ...base, current: 5, previous: null }), null);
  });
});
