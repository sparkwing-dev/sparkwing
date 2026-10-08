import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Run } from "@/lib/api";
import type { RunFilterState } from "@/components/RunFilters";
import {
  cursorForRun,
  encodeCursor,
  pageFromResponse,
  pageRefFromParams,
  pageRefParams,
  parseCursor,
  runListQuery,
} from "./runsPaging";

type FilterValues = Omit<RunFilterState, `set${string}`>;

function filters(overrides: Partial<FilterValues> = {}): FilterValues {
  return {
    filterTrigger: [],
    excludeTrigger: [],
    filterStatus: [],
    excludeStatus: [],
    filterRepo: [],
    excludeRepo: [],
    filterPipeline: [],
    excludePipeline: [],
    filterBranch: [],
    excludeBranch: [],
    filterCommit: [],
    excludeCommit: [],
    filterTag: [],
    excludeTag: [],
    startedAfter: "",
    startedBefore: "",
    finishedAfter: "",
    finishedBefore: "",
    filterText: "",
    ...overrides,
  };
}

function run(id: string, started_at: string): Run {
  return { id, pipeline: "build", status: "success", started_at } as Run;
}

function runs(n: number, from = 0): Run[] {
  return Array.from({ length: n }, (_, i) =>
    run(
      `run-${from + i}`,
      `2026-10-08T12:00:${String(59 - ((from + i) % 60)).padStart(2, "0")}Z`,
    ),
  );
}

describe("cursorForRun", () => {
  it("keeps every nanosecond of the controller's instant", () => {
    assert.deepEqual(cursorForRun(run("r", "2026-10-08T17:00:50.123456789Z")), {
      startedAt: "1791478850123456789",
      id: "r",
    });
  });

  it("reads trimmed fractions and zone offsets", () => {
    assert.equal(
      cursorForRun(run("r", "2026-10-08T11:00:50.5-06:00")).startedAt,
      "1791478850500000000",
    );
    assert.equal(
      cursorForRun(run("r", "2026-10-08T17:00:50Z")).startedAt,
      "1791478850000000000",
    );
  });

  it("files a run that never started under instant zero", () => {
    assert.equal(cursorForRun(run("r", "0001-01-01T00:00:00Z")).startedAt, "0");
  });
});

describe("page references in the URL", () => {
  it("round-trips a cursor whose id holds the separator", () => {
    const cursor = { startedAt: "1791478850123456789", id: "run:with:colons" };
    assert.deepEqual(parseCursor(encodeCursor(cursor)), cursor);
  });

  it("rejects malformed cursors and falls back to the newest page", () => {
    for (const bad of ["", "abc:run", ":run", "123:", "123"]) {
      assert.equal(parseCursor(bad), null, bad);
    }
    assert.deepEqual(pageRefFromParams(new URLSearchParams("older=nonsense")), {
      kind: "latest",
    });
  });

  it("writes one page parameter and clears the other", () => {
    const cursor = { startedAt: "5", id: "r" };
    assert.deepEqual(pageRefParams({ kind: "older", cursor }), {
      older: "5:r",
      newer: null,
    });
    assert.deepEqual(pageRefParams({ kind: "latest" }), {
      older: null,
      newer: null,
    });
    assert.deepEqual(pageRefFromParams(new URLSearchParams("newer=5:r")), {
      kind: "newer",
      cursor,
    });
  });
});

describe("runListQuery", () => {
  it("sends every filter to the controller", () => {
    const q = runListQuery(
      filters({
        filterStatus: ["failed"],
        excludeStatus: ["cancelled"],
        filterTrigger: ["push"],
        excludeTrigger: ["cron"],
        filterRepo: ["web"],
        excludeRepo: ["api"],
        filterPipeline: ["build"],
        excludePipeline: ["lint"],
        filterBranch: ["main"],
        excludeBranch: ["wip"],
        filterCommit: ["abc1234"],
        excludeCommit: ["def5678"],
        startedAfter: "2026-10-01T00:00:00Z",
        filterText: " deploy -flaky ",
      }),
      {},
      { kind: "latest" },
    );
    assert.ok(q);
    assert.deepEqual(Object.fromEntries(q), {
      pipeline: "build",
      exclude_pipeline: "lint",
      status: "failed",
      exclude_status: "cancelled",
      trigger_source: "push",
      exclude_trigger_source: "cron",
      repo_name: "web",
      exclude_repo_name: "api",
      git_branch: "main",
      exclude_git_branch: "wip",
      git_sha: "abc1234",
      exclude_git_sha: "def5678",
      started_after: "2026-10-01T00:00:00Z",
      q: "deploy -flaky",
      limit: "51",
    });
  });

  it("passes a full RFC 3339 bound through with every nanosecond", () => {
    const q = runListQuery(
      filters({
        startedBefore: "2026-10-08T17:00:50.123456789Z",
        finishedAfter: " 2026-10-08T11:00:50.5-06:00 ",
        finishedBefore: "2026-10-08",
      }),
      {},
      { kind: "latest" },
    );
    assert.equal(q?.get("started_before"), "2026-10-08T17:00:50.123456789Z");
    assert.equal(q?.get("finished_after"), "2026-10-08T11:00:50.5-06:00");
    assert.equal(q?.get("finished_before"), new Date("2026-10-08T00:00").toISOString());
  });

  it("lets Date normalize an RFC 3339 time with an out-of-range field", () => {
    for (const [raw, want] of [
      ["2026-10-08T24:00:00Z", "2026-10-09T00:00:00.000Z"],
      ["2026-02-30T00:00:00Z", "2026-03-02T00:00:00.000Z"],
    ]) {
      const q = runListQuery(filters({ startedBefore: raw }), {}, { kind: "latest" });
      assert.equal(q?.get("started_before"), want, raw);
    }
    for (const raw of ["2024-02-29T23:59:59.999999999Z", "2026-12-31T00:00:00+23:59"]) {
      const q = runListQuery(filters({ startedBefore: raw }), {}, { kind: "latest" });
      assert.equal(q?.get("started_before"), raw);
    }
    for (const raw of ["2026-13-01T00:00:00Z", "2026-10-08T12:60:00Z"]) {
      const q = runListQuery(filters({ startedBefore: raw }), {}, { kind: "latest" });
      assert.equal(q?.get("started_before"), null, raw);
    }
  });

  it("turns tags into the pipelines that carry them", () => {
    const meta = {
      build: { tags: ["ci"] },
      deploy: { tags: ["release"] },
      lint: { tags: ["ci", "fast"] },
    } as never;
    assert.equal(
      runListQuery(filters({ filterTag: ["ci"], excludeTag: ["fast"] }), meta, {
        kind: "latest",
      })?.toString(),
      "pipeline=build%2Clint&exclude_pipeline=lint&limit=51",
    );
    assert.equal(
      runListQuery(
        filters({ filterTag: ["ci"], filterPipeline: ["deploy", "lint"] }),
        meta,
        { kind: "latest" },
      )?.get("pipeline"),
      "lint",
    );
    assert.equal(
      runListQuery(
        filters({ filterTag: ["ci"], filterPipeline: ["deploy"] }),
        meta,
        { kind: "latest" },
      ),
      null,
    );
  });

  it("pages by cursor in either direction", () => {
    const cursor = { startedAt: "1791478850123456789", id: "run-7" };
    const older = runListQuery(filters(), {}, { kind: "older", cursor });
    assert.equal(older?.get("after_started_at"), cursor.startedAt);
    assert.equal(older?.get("after_id"), "run-7");
    const newer = runListQuery(filters(), {}, { kind: "newer", cursor });
    assert.equal(newer?.get("before_started_at"), cursor.startedAt);
    assert.equal(newer?.get("before_id"), "run-7");
  });
});

describe("pageFromResponse", () => {
  it("offers an older page only when the probe row came back", () => {
    const full = pageFromResponse(runs(51), { kind: "latest" });
    assert.equal(full.runs.length, 50);
    assert.deepEqual(full.older, {
      kind: "older",
      cursor: cursorForRun(full.runs[49]),
    });
    assert.equal(full.newer, null);
    assert.equal(full.atLatest, true);

    const last = pageFromResponse(runs(12), {
      kind: "older",
      cursor: { startedAt: "9", id: "x" },
    });
    assert.equal(last.older, null);
    assert.deepEqual(last.newer, {
      kind: "newer",
      cursor: cursorForRun(last.runs[0]),
    });
  });

  it("walks back toward the newest page and says when it got there", () => {
    const cursor = { startedAt: "9", id: "x" };
    const middle = pageFromResponse(runs(51), { kind: "newer", cursor });
    assert.equal(middle.runs[0].id, "run-1");
    assert.equal(middle.runs.length, 50);
    assert.deepEqual(middle.newer, {
      kind: "newer",
      cursor: cursorForRun(middle.runs[0]),
    });
    assert.deepEqual(middle.older, {
      kind: "older",
      cursor: cursorForRun(middle.runs[49]),
    });
    assert.equal(middle.atLatest, false);

    const newest = pageFromResponse(runs(20), { kind: "newer", cursor });
    assert.equal(newest.newer, null);
    assert.equal(newest.atLatest, true);
  });

  it("keeps a way back from an empty page", () => {
    const cursor = { startedAt: "9", id: "x" };
    const empty = pageFromResponse([], { kind: "older", cursor });
    assert.deepEqual(empty.newer, { kind: "newer", cursor });
    assert.equal(empty.older, null);
  });
});
